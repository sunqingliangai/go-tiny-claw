package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/sunqingliangai/go-tiny-claw/internal/engine"
)

const maxReplyRuneCount = 12000

type feishuReporter struct {
	client        *lark.Client
	messageID     string
	thinkingOnce  sync.Once
	toolErrorOnce sync.Once
	sendMu        sync.Mutex
	sequence      atomic.Uint64
}

func newFeishuReporter(client *lark.Client, messageID string) *feishuReporter {
	return &feishuReporter{
		client:    client,
		messageID: messageID,
	}
}

func (r *feishuReporter) OnThinking(ctx context.Context) {
	r.thinkingOnce.Do(func() {
		r.send(ctx, "收到，正在处理...")
	})
}

func (r *feishuReporter) OnToolCall(_ context.Context, toolName string, _ string) {
	// 工具开始事件只记录日志，避免并行 Tool Call 刷屏并触发飞书限频。
	log.Printf("[Feishu] 工具开始 message_id=%s tool=%s", r.messageID, toolName)
}

func (r *feishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if !isError {
		return
	}
	r.toolErrorOnce.Do(func() {
		r.send(ctx, fmt.Sprintf("工具执行失败（%s）:\n%s", toolName, result))
	})
}

func (r *feishuReporter) OnMessage(ctx context.Context, content string) {
	r.send(ctx, content)
}

func (r *feishuReporter) SendError(content string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.send(ctx, content)
}

func (r *feishuReporter) send(ctx context.Context, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}

	r.sendMu.Lock()
	defer r.sendMu.Unlock()

	uuid := makeReplyUUID(r.messageID, "reply", r.sequence.Add(1))
	if err := replyTextWithUUID(ctx, r.client, r.messageID, uuid, content); err != nil {
		log.Printf("[Feishu] 回复失败 message_id=%s: %v", r.messageID, err)
	}
}

func replyText(ctx context.Context, client *lark.Client, messageID, kind, text string) error {
	return replyTextWithUUID(ctx, client, messageID, makeReplyUUID(messageID, kind, 0), text)
}

func replyTextWithUUID(ctx context.Context, client *lark.Client, messageID, uuid, text string) error {
	text = truncateRunes(text, maxReplyRuneCount)
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(string(content)).
			Uuid(uuid).
			Build()).
		Build()

	resp, err := client.Im.Message.Reply(ctx, req)
	if err != nil {
		return fmt.Errorf("调用飞书回复接口失败: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("飞书回复接口返回空响应")
	}
	if !resp.Success() {
		return fmt.Errorf("飞书回复失败 code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func makeReplyUUID(messageID, kind string, sequence uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", messageID, kind, sequence)))
	return fmt.Sprintf("%x", sum[:16])
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n...（消息过长，已截断）"
}

var _ engine.Reporter = (*feishuReporter)(nil)
