package feishu

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

const (
	defaultApprovalTimeout = 5 * time.Minute
	approvalNoticeTimeout  = 10 * time.Second
)

type approvalRunInfo struct {
	ChatID          string
	RequesterID     string
	SourceMessageID string
}

type approvalRunContextKey struct{}

func withApprovalRunInfo(ctx context.Context, info approvalRunInfo) context.Context {
	return context.WithValue(ctx, approvalRunContextKey{}, info)
}

func approvalRunInfoFromContext(ctx context.Context) (approvalRunInfo, bool) {
	info, ok := ctx.Value(approvalRunContextKey{}).(approvalRunInfo)
	return info, ok && info.ChatID != "" && info.RequesterID != "" && info.SourceMessageID != ""
}

type approvalNotice struct {
	ApprovalID      string
	ChatID          string
	SourceMessageID string
	ToolName        string
	Arguments       string
	Reason          string
	ExpiresAt       time.Time
}

type approvalNotifier func(ctx context.Context, notice approvalNotice) error

type pendingApproval struct {
	ApprovalID  string
	ChatID      string
	RequesterID string
	Fingerprint string
	ExpiresAt   time.Time
	Result      chan tools.ApprovalDecision
}

type approvalManager struct {
	mu          sync.Mutex
	pending     map[string]*pendingApproval
	approverIDs map[string]struct{}
	timeout     time.Duration
	notify      approvalNotifier
	generateID  func() (string, error)
	currentTime func() time.Time
}

func newApprovalManager(approverIDs []string, notify approvalNotifier) *approvalManager {
	allowed := make(map[string]struct{}, len(approverIDs))
	for _, id := range approverIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			allowed[id] = struct{}{}
		}
	}
	return &approvalManager{
		pending:     make(map[string]*pendingApproval),
		approverIDs: allowed,
		timeout:     defaultApprovalTimeout,
		notify:      notify,
		generateID:  newApprovalID,
		currentTime: time.Now,
	}
}

// RequestApproval 发送飞书审批请求并阻塞等待一次性结果、超时或 Agent Run 取消。
func (m *approvalManager) RequestApproval(
	ctx context.Context,
	request tools.ApprovalRequest,
) (tools.ApprovalDecision, error) {
	info, ok := approvalRunInfoFromContext(ctx)
	if !ok {
		return tools.ApprovalDecision{}, fmt.Errorf("缺少审批所需的聊天或用户上下文")
	}

	m.mu.Lock()
	approvalID, err := m.generateUniqueIDLocked()
	if err != nil {
		m.mu.Unlock()
		return tools.ApprovalDecision{}, err
	}
	pending := &pendingApproval{
		ApprovalID:  approvalID,
		ChatID:      info.ChatID,
		RequesterID: info.RequesterID,
		Fingerprint: tools.ToolCallFingerprint(request.ToolCall),
		ExpiresAt:   m.currentTime().Add(m.timeout),
		Result:      make(chan tools.ApprovalDecision, 1),
	}
	m.pending[approvalID] = pending
	m.mu.Unlock()

	notice := approvalNotice{
		ApprovalID:      approvalID,
		ChatID:          info.ChatID,
		SourceMessageID: info.SourceMessageID,
		ToolName:        request.ToolCall.Name,
		Arguments:       string(request.ToolCall.Arguments),
		Reason:          request.Reason,
		ExpiresAt:       pending.ExpiresAt,
	}

	notifyCtx, cancelNotify := context.WithTimeout(ctx, approvalNoticeTimeout)
	err = m.notify(notifyCtx, notice)
	cancelNotify()
	if err != nil {
		m.removePending(approvalID)
		return tools.ApprovalDecision{}, fmt.Errorf("发送审批请求失败: %w", err)
	}

	log.Printf(
		"[Approval] 已发送审批请求 approval_id=%s chat_id=%s tool=%s",
		approvalID,
		info.ChatID,
		request.ToolCall.Name,
	)

	remaining := pending.ExpiresAt.Sub(m.currentTime())
	waitCtx, cancelWait := context.WithTimeout(ctx, remaining)
	defer cancelWait()

	select {
	case decision := <-pending.Result:
		return decision, nil

	case <-waitCtx.Done():
		m.mu.Lock()
		_, stillPending := m.pending[approvalID]
		if stillPending {
			delete(m.pending, approvalID)
		}
		m.mu.Unlock()

		// ResolveApproval 会先删除 pending、再向缓冲 Channel 写入决定。
		// 若超时与审批恰好同时发生，以已提交的人工决定为准。
		if !stillPending {
			return <-pending.Result, nil
		}
		if ctx.Err() != nil {
			return tools.ApprovalDecision{
				Allowed: false,
				Reason:  "Agent 运行已取消，审批请求失效。",
				Code:    schema.ErrorCodeApprovalCanceled,
			}, nil
		}
		return tools.ApprovalDecision{
			Allowed: false,
			Reason:  "人工审批超时，操作未执行。",
			Code:    schema.ErrorCodeApprovalTimeout,
		}, nil
	}
}

// ResolveApproval 消费一次性 token，并返回给审批命令发送者的提示文本。
func (m *approvalManager) ResolveApproval(
	chatID string,
	senderID string,
	approvalID string,
	allowed bool,
) string {
	approvalID = strings.TrimSpace(approvalID)

	m.mu.Lock()
	pending, exists := m.pending[approvalID]
	if !exists {
		m.mu.Unlock()
		return "审批不存在、已完成或已过期。"
	}
	if pending.ChatID != chatID {
		m.mu.Unlock()
		return "审批与当前聊天不匹配，已忽略。"
	}
	if !m.canApproveLocked(pending, senderID) {
		m.mu.Unlock()
		return "你没有权限审批该操作。"
	}
	if !m.currentTime().Before(pending.ExpiresAt) {
		delete(m.pending, approvalID)
		pending.Result <- tools.ApprovalDecision{
			Allowed: false,
			Reason:  "人工审批超时，操作未执行。",
			Code:    schema.ErrorCodeApprovalTimeout,
		}
		m.mu.Unlock()
		return "审批已过期，操作不会执行。"
	}
	delete(m.pending, approvalID)

	if allowed {
		pending.Result <- tools.ApprovalDecision{
			Allowed:     true,
			Reason:      "用户已批准本次操作。",
			Fingerprint: pending.Fingerprint,
		}
		m.mu.Unlock()
		log.Printf("[Approval] ✅ 已批准 approval_id=%s sender_id=%s", approvalID, senderID)
		return "已批准本次操作。"
	}

	pending.Result <- tools.ApprovalDecision{
		Allowed:     false,
		Reason:      "用户拒绝执行该操作。",
		Code:        schema.ErrorCodeApprovalRejected,
		Fingerprint: pending.Fingerprint,
	}
	m.mu.Unlock()
	log.Printf("[Approval] 🚫 已拒绝 approval_id=%s sender_id=%s", approvalID, senderID)
	return "已拒绝本次操作。"
}

func (m *approvalManager) canApproveLocked(pending *pendingApproval, senderID string) bool {
	if senderID == pending.RequesterID {
		return true
	}
	_, allowed := m.approverIDs[senderID]
	return allowed
}

func (m *approvalManager) generateUniqueIDLocked() (string, error) {
	for i := 0; i < 3; i++ {
		id, err := m.generateID()
		if err != nil {
			return "", fmt.Errorf("生成审批 ID 失败: %w", err)
		}
		if _, exists := m.pending[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("生成审批 ID 冲突")
}

func (m *approvalManager) removePending(approvalID string) {
	m.mu.Lock()
	delete(m.pending, approvalID)
	m.mu.Unlock()
}

func newApprovalID() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "apr_" + base64.RawURLEncoding.EncodeToString(random), nil
}

var _ tools.Approver = (*approvalManager)(nil)
