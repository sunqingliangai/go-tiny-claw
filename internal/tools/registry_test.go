package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

type stubTool struct {
	err      error
	executed *bool
}

func (t *stubTool) Name() string {
	return "stub"
}

func (t *stubTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name()}
}

func (t *stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	if t.executed != nil {
		*t.executed = true
	}
	return "", t.err
}

type stubApprover struct {
	decision ApprovalDecision
	err      error
	requests []ApprovalRequest
}

func (a *stubApprover) RequestApproval(
	_ context.Context,
	request ApprovalRequest,
) (ApprovalDecision, error) {
	a.requests = append(a.requests, request)
	return a.decision, a.err
}

func TestRegistryPreservesStructuredToolError(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&stubTool{
		err: &ToolError{
			Code:      schema.ErrorCodeEditOldTextAmbiguous,
			Message:   "匹配到了多处",
			Output:    "file content",
			Retryable: true,
		},
	})

	result := registry.Execute(context.Background(), schema.ToolCall{
		ID:   "call-1",
		Name: "stub",
	})

	if !result.IsError {
		t.Fatal("expected error result")
	}
	if result.ErrorCode != schema.ErrorCodeEditOldTextAmbiguous {
		t.Fatalf("unexpected error code: %s", result.ErrorCode)
	}
	if !result.Retryable {
		t.Fatal("expected retryable error")
	}
	if !strings.Contains(result.Output, "file content") {
		t.Fatalf("tool output was lost: %q", result.Output)
	}
}

func TestRegistryMiddlewareDenyPreventsExecution(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{
			Effect:    PolicyDeny,
			Reason:    "测试策略拒绝",
			ErrorCode: schema.ErrorCodePolicyDenied,
		}, nil
	})

	result := registry.Execute(context.Background(), schema.ToolCall{
		ID:   "call-denied",
		Name: "stub",
	})

	if executed {
		t.Fatal("denied tool must not execute")
	}
	if !result.IsError || result.ErrorCode != schema.ErrorCodePolicyDenied {
		t.Fatalf("unexpected deny result: %#v", result)
	}
	if !strings.Contains(result.Output, "测试策略拒绝") {
		t.Fatalf("deny reason missing: %q", result.Output)
	}
}

func TestRegistryMiddlewareErrorFailsClosed(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{}, errors.New("policy unavailable")
	})

	result := registry.Execute(context.Background(), schema.ToolCall{
		ID:   "call-policy-error",
		Name: "stub",
	})

	if executed {
		t.Fatal("middleware error must fail closed")
	}
	if !result.IsError || result.ErrorCode != schema.ErrorCodeToolInternal {
		t.Fatalf("unexpected fail-closed result: %#v", result)
	}
}

func TestRegistryApprovalAllowExecutesOnce(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{Effect: PolicyAsk, Reason: "需要审批"}, nil
	})

	approver := &stubApprover{
		decision: ApprovalDecision{
			Allowed:     true,
			Reason:      "approved",
			Fingerprint: ToolCallFingerprint(schema.ToolCall{ID: "call-allow", Name: "stub"}),
		},
	}
	ctx := WithApprover(context.Background(), approver)
	result := registry.Execute(ctx, schema.ToolCall{ID: "call-allow", Name: "stub"})

	if !executed {
		t.Fatal("approved tool should execute")
	}
	if result.IsError {
		t.Fatalf("expected success result: %#v", result)
	}
	if len(approver.requests) != 1 {
		t.Fatalf("expected one approval request, got %d", len(approver.requests))
	}
}

func TestRegistryApprovalRejectPreventsExecution(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{Effect: PolicyAsk, Reason: "需要审批"}, nil
	})

	approver := &stubApprover{
		decision: ApprovalDecision{
			Allowed: false,
			Reason:  "用户拒绝",
			Code:    schema.ErrorCodeApprovalRejected,
		},
	}
	result := registry.Execute(
		WithApprover(context.Background(), approver),
		schema.ToolCall{ID: "call-reject", Name: "stub"},
	)

	if executed {
		t.Fatal("rejected tool must not execute")
	}
	if !result.IsError || result.ErrorCode != schema.ErrorCodeApprovalRejected {
		t.Fatalf("unexpected rejection result: %#v", result)
	}
}

func TestRegistryAskWithoutApproverFailsClosed(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{Effect: PolicyAsk}, nil
	})

	result := registry.Execute(context.Background(), schema.ToolCall{ID: "call-no-approver", Name: "stub"})
	if executed {
		t.Fatal("tool without approval channel must not execute")
	}
	if !result.IsError || result.ErrorCode != schema.ErrorCodeApprovalUnavailable {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
}

func TestRegistryRejectsApprovalFingerprintMismatch(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{Effect: PolicyAsk}, nil
	})

	approver := &stubApprover{
		decision: ApprovalDecision{
			Allowed:     true,
			Fingerprint: "different-action",
		},
	}
	result := registry.Execute(
		WithApprover(context.Background(), approver),
		schema.ToolCall{ID: "call-mismatch", Name: "stub"},
	)

	if executed {
		t.Fatal("mismatched approval must not execute")
	}
	if !result.IsError || result.ErrorCode != schema.ErrorCodeApprovalMismatch {
		t.Fatalf("unexpected mismatch result: %#v", result)
	}
}

func TestRegistryCancellationWinsAfterApproval(t *testing.T) {
	executed := false
	registry := NewRegistry()
	registry.Register(&stubTool{executed: &executed})
	registry.Use(func(context.Context, schema.ToolCall) (MiddlewareDecision, error) {
		return MiddlewareDecision{Effect: PolicyAsk}, nil
	})

	call := schema.ToolCall{ID: "call-canceled", Name: "stub"}
	approver := &stubApprover{
		decision: ApprovalDecision{
			Allowed:     true,
			Fingerprint: ToolCallFingerprint(call),
		},
	}
	ctx, cancel := context.WithCancel(WithApprover(context.Background(), approver))
	cancel()

	result := registry.Execute(ctx, call)
	if executed {
		t.Fatal("canceled run must not execute approved tool")
	}
	if !result.IsError || result.ErrorCode != schema.ErrorCodeApprovalCanceled {
		t.Fatalf("unexpected cancellation result: %#v", result)
	}
}
