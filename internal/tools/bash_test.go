package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

func TestBashToolMarksNonzeroExitAsStructuredError(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"exit 7"}`))
	if err == nil {
		t.Fatal("expected nonzero exit error")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %T", err)
	}
	if toolErr.Code != schema.ErrorCodeBashExitNonzero {
		t.Fatalf("unexpected error code: %s", toolErr.Code)
	}
}

func TestBashToolMarksMissingCommandAsStructuredError(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	_, err := tool.Execute(
		context.Background(),
		json.RawMessage(`{"command":"definitely-not-a-real-command-for-tiny-claw"}`),
	)
	if err == nil {
		t.Fatal("expected command-not-found error")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %T", err)
	}
	if toolErr.Code != schema.ErrorCodeBashCommandNotFound {
		t.Fatalf("unexpected error code: %s", toolErr.Code)
	}
}
