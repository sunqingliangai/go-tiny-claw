package tools

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

// PolicyEffect 是执行前策略的最终效果。
type PolicyEffect string

const (
	PolicyAllow PolicyEffect = "allow"
	PolicyAsk   PolicyEffect = "ask"
	PolicyDeny  PolicyEffect = "deny"
)

// MiddlewareDecision 描述一条策略对当前 ToolCall 的判断。
type MiddlewareDecision struct {
	Effect    PolicyEffect
	Reason    string
	ErrorCode schema.ErrorCode
}

// MiddlewareFunc 在 Tool 真正执行前运行。
// 返回 error 表示策略或审批基础设施故障，Registry 会按 fail-closed 处理。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (MiddlewareDecision, error)

// ApprovalRequest 是与传输层无关的人工审批请求。
type ApprovalRequest struct {
	ToolCall schema.ToolCall
	Reason   string
}

// ApprovalDecision 是人工审批的最终结果。
type ApprovalDecision struct {
	Allowed     bool
	Reason      string
	Code        schema.ErrorCode
	Fingerprint string
}

// Approver 由飞书等人工审批适配器实现。
type Approver interface {
	RequestApproval(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error)
}

type approverContextKey struct{}

// WithApprover 将当前 Agent Run 的审批适配器写入 Context。
func WithApprover(ctx context.Context, approver Approver) context.Context {
	return context.WithValue(ctx, approverContextKey{}, approver)
}

func approverFromContext(ctx context.Context) (Approver, bool) {
	approver, ok := ctx.Value(approverContextKey{}).(Approver)
	return approver, ok && approver != nil
}

// ToolCallFingerprint 绑定审批时的工具名与原始参数。
// Registry 会在放行前再次比较，避免审批结果与当前动作不一致。
func ToolCallFingerprint(call schema.ToolCall) string {
	sum := sha256.Sum256([]byte(call.Name + "\x00" + string(call.Arguments)))
	return fmt.Sprintf("%x", sum)
}
