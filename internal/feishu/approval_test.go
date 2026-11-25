package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

func TestApprovalManagerBindsChatAndUserAndConsumesOnce(t *testing.T) {
	notices := make(chan approvalNotice, 1)
	manager := newApprovalManager(nil, func(_ context.Context, notice approvalNotice) error {
		notices <- notice
		return nil
	})
	manager.timeout = time.Second

	resultCh := make(chan tools.ApprovalDecision, 1)
	errCh := make(chan error, 1)
	go func() {
		decision, err := manager.RequestApproval(approvalTestContext(), approvalTestRequest())
		resultCh <- decision
		errCh <- err
	}()

	notice := receiveApprovalNotice(t, notices)
	if response := manager.ResolveApproval(notice.ChatID, "another-user", notice.ApprovalID, true); !strings.Contains(response, "没有权限") {
		t.Fatalf("unexpected unauthorized response: %q", response)
	}
	if response := manager.ResolveApproval("another-chat", "requester", notice.ApprovalID, true); !strings.Contains(response, "聊天不匹配") {
		t.Fatalf("unexpected chat mismatch response: %q", response)
	}

	if response := manager.ResolveApproval(notice.ChatID, "requester", notice.ApprovalID, true); !strings.Contains(response, "已批准") {
		t.Fatalf("unexpected approve response: %q", response)
	}
	decision := receiveApprovalDecision(t, resultCh)
	if err := <-errCh; err != nil {
		t.Fatalf("approval request failed: %v", err)
	}
	if !decision.Allowed || decision.Fingerprint == "" {
		t.Fatalf("unexpected approval decision: %#v", decision)
	}

	if response := manager.ResolveApproval(notice.ChatID, "requester", notice.ApprovalID, true); !strings.Contains(response, "不存在") {
		t.Fatalf("second approval should not resolve: %q", response)
	}
}

func TestApprovalManagerAllowsConfiguredApprover(t *testing.T) {
	notices := make(chan approvalNotice, 1)
	manager := newApprovalManager([]string{"admin-user"}, func(_ context.Context, notice approvalNotice) error {
		notices <- notice
		return nil
	})

	resultCh := make(chan tools.ApprovalDecision, 1)
	go func() {
		decision, _ := manager.RequestApproval(approvalTestContext(), approvalTestRequest())
		resultCh <- decision
	}()

	notice := receiveApprovalNotice(t, notices)
	manager.ResolveApproval(notice.ChatID, "admin-user", notice.ApprovalID, true)
	if decision := receiveApprovalDecision(t, resultCh); !decision.Allowed {
		t.Fatalf("configured approver should allow the request: %#v", decision)
	}
}

func TestApprovalManagerRejectsAndTimesOut(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		notices := make(chan approvalNotice, 1)
		manager := newApprovalManager(nil, func(_ context.Context, notice approvalNotice) error {
			notices <- notice
			return nil
		})

		resultCh := make(chan tools.ApprovalDecision, 1)
		go func() {
			decision, _ := manager.RequestApproval(approvalTestContext(), approvalTestRequest())
			resultCh <- decision
		}()

		notice := receiveApprovalNotice(t, notices)
		manager.ResolveApproval(notice.ChatID, "requester", notice.ApprovalID, false)
		decision := receiveApprovalDecision(t, resultCh)
		if decision.Allowed || decision.Code != schema.ErrorCodeApprovalRejected {
			t.Fatalf("unexpected rejection decision: %#v", decision)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		manager := newApprovalManager(nil, func(context.Context, approvalNotice) error {
			return nil
		})
		manager.timeout = 20 * time.Millisecond

		decision, err := manager.RequestApproval(approvalTestContext(), approvalTestRequest())
		if err != nil {
			t.Fatalf("timeout should be a decision, not infrastructure error: %v", err)
		}
		if decision.Allowed || decision.Code != schema.ErrorCodeApprovalTimeout {
			t.Fatalf("unexpected timeout decision: %#v", decision)
		}
	})
}

func TestApprovalManagerCancelsWithRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(approvalTestContext())
	defer cancel()

	manager := newApprovalManager(nil, func(context.Context, approvalNotice) error {
		cancel()
		return nil
	})
	manager.timeout = time.Second

	decision, err := manager.RequestApproval(ctx, approvalTestRequest())
	if err != nil {
		t.Fatalf("cancel should be a decision, not infrastructure error: %v", err)
	}
	if decision.Allowed || decision.Code != schema.ErrorCodeApprovalCanceled {
		t.Fatalf("unexpected cancellation decision: %#v", decision)
	}
}

func TestParseApprovalCommand(t *testing.T) {
	tests := []struct {
		prompt    string
		wantID    string
		wantAllow bool
		wantOK    bool
	}{
		{prompt: "approve apr_123", wantID: "apr_123", wantAllow: true, wantOK: true},
		{prompt: "REJECT apr_456", wantID: "apr_456", wantAllow: false, wantOK: true},
		{prompt: "approve", wantOK: false},
		{prompt: "approve apr_1 extra", wantOK: false},
		{prompt: "please approve apr_1", wantOK: false},
	}

	for _, test := range tests {
		approvalID, allowed, ok := parseApprovalCommand(test.prompt)
		if approvalID != test.wantID || allowed != test.wantAllow || ok != test.wantOK {
			t.Fatalf(
				"parseApprovalCommand(%q) = (%q,%v,%v), want (%q,%v,%v)",
				test.prompt,
				approvalID,
				allowed,
				ok,
				test.wantID,
				test.wantAllow,
				test.wantOK,
			)
		}
	}
}

func TestResolveFeishuSenderID(t *testing.T) {
	value := func(text string) *string { return &text }

	sender := &larkim.EventSender{
		SenderId: &larkim.UserId{
			UserId:  value("user-id"),
			OpenId:  value("open-id"),
			UnionId: value("union-id"),
		},
	}
	if id, ok := resolveFeishuSenderID(sender); !ok || id != "user-id" {
		t.Fatalf("unexpected user_id result: id=%q ok=%v", id, ok)
	}

	sender.SenderId.UserId = nil
	if id, ok := resolveFeishuSenderID(sender); !ok || id != "open-id" {
		t.Fatalf("unexpected open_id fallback: id=%q ok=%v", id, ok)
	}

	sender.SenderId.OpenId = nil
	if id, ok := resolveFeishuSenderID(sender); !ok || id != "union-id" {
		t.Fatalf("unexpected union_id fallback: id=%q ok=%v", id, ok)
	}

	sender.SenderId = nil
	if _, ok := resolveFeishuSenderID(sender); ok {
		t.Fatal("sender without SenderId should be rejected")
	}
}

func approvalTestContext() context.Context {
	return withApprovalRunInfo(context.Background(), approvalRunInfo{
		ChatID:          "chat-1",
		RequesterID:     "requester",
		SourceMessageID: "message-1",
	})
}

func approvalTestRequest() tools.ApprovalRequest {
	return tools.ApprovalRequest{
		ToolCall: schema.ToolCall{
			ID:        "call-1",
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":"rm -rf build"}`),
		},
		Reason: "测试审批",
	}
}

func receiveApprovalNotice(t *testing.T, notices <-chan approvalNotice) approvalNotice {
	t.Helper()
	select {
	case notice := <-notices:
		return notice
	case <-time.After(time.Second):
		t.Fatal("approval notice was not sent")
		return approvalNotice{}
	}
}

func receiveApprovalDecision(
	t *testing.T,
	results <-chan tools.ApprovalDecision,
) tools.ApprovalDecision {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("approval decision was not returned")
		return tools.ApprovalDecision{}
	}
}
