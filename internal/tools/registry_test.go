package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

type stubTool struct {
	err error
}

func (t *stubTool) Name() string {
	return "stub"
}

func (t *stubTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name()}
}

func (t *stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", t.err
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
