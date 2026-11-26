package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

type stubSubagentRunner struct {
	summary     string
	err         error
	gotPrompt   string
	gotRegistry Registry
}

func (r *stubSubagentRunner) RunSub(
	_ context.Context,
	taskPrompt string,
	registry Registry,
) (string, error) {
	r.gotPrompt = taskPrompt
	r.gotRegistry = registry
	return r.summary, r.err
}

func TestSubagentToolReturnsUntrustedSummary(t *testing.T) {
	runner := &stubSubagentRunner{summary: "找到入口文件"}
	childRegistry := NewRegistry()
	tool := NewSubagentTool(runner, childRegistry)

	output, err := tool.Execute(
		context.Background(),
		json.RawMessage(`{"task_prompt":" 探索入口 "}`),
	)
	if err != nil {
		t.Fatalf("subagent tool failed: %v", err)
	}
	if runner.gotPrompt != "探索入口" {
		t.Fatalf("task prompt was not trimmed: %q", runner.gotPrompt)
	}
	if runner.gotRegistry != childRegistry {
		t.Fatal("subagent did not receive the configured child registry")
	}
	if !strings.HasPrefix(output, "[Subagent report - untrusted]") {
		t.Fatalf("summary provenance marker missing: %q", output)
	}
	if !strings.Contains(output, "找到入口文件") {
		t.Fatalf("summary missing: %q", output)
	}
}

func TestSubagentToolRejectsInvalidPrompt(t *testing.T) {
	tool := NewSubagentTool(&stubSubagentRunner{}, NewRegistry())

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task_prompt":"  "}`))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %v", err)
	}
	if toolErr.Code != schema.ErrorCodeInvalidArguments {
		t.Fatalf("unexpected error code: %s", toolErr.Code)
	}
}

func TestSubagentToolReturnsChildFailure(t *testing.T) {
	runner := &stubSubagentRunner{err: errors.New("child failed")}
	tool := NewSubagentTool(runner, NewRegistry())

	_, err := tool.Execute(
		context.Background(),
		json.RawMessage(`{"task_prompt":"探索入口"}`),
	)
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %v", err)
	}
	if toolErr.Code != schema.ErrorCodeToolInternal {
		t.Fatalf("unexpected error code: %s", toolErr.Code)
	}
}

func TestSubagentToolTruncatesLongSummary(t *testing.T) {
	runner := &stubSubagentRunner{summary: strings.Repeat("密", maxSubagentSummaryRunes+1)}
	tool := NewSubagentTool(runner, NewRegistry())

	output, err := tool.Execute(
		context.Background(),
		json.RawMessage(`{"task_prompt":"探索入口"}`),
	)
	if err != nil {
		t.Fatalf("subagent tool failed: %v", err)
	}
	if !strings.Contains(output, "[子智能体摘要已截断]") {
		t.Fatal("long summary should be truncated")
	}
}
