package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

type runSubProvider struct {
	responses []*schema.Message
	calls     int
}

func (p *runSubProvider) Generate(
	_ context.Context,
	_ []schema.Message,
	_ []schema.ToolDefinition,
) (*schema.Message, error) {
	response := p.responses[p.calls]
	p.calls++
	return response, nil
}

type runSubProbeTool struct {
	executions int
}

func (t *runSubProbeTool) Name() string {
	return "probe"
}

func (t *runSubProbeTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.Name()}
}

func (t *runSubProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.executions++
	return "probe-result", nil
}

func TestRunSubUsesIsolatedHistoryAndReturnsSummary(t *testing.T) {
	provider := &runSubProvider{
		responses: []*schema.Message{
			{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)},
				},
			},
			{
				Role:    schema.RoleAssistant,
				Content: "最终探索摘要",
			},
		},
	}
	probe := &runSubProbeTool{}
	childRegistry := tools.NewRegistry()
	childRegistry.Register(probe)
	engine := NewAgentEngine(provider, childRegistry, false)

	summary, err := engine.RunSub(context.Background(), "探索入口", childRegistry)
	if err != nil {
		t.Fatalf("RunSub failed: %v", err)
	}
	if summary != "最终探索摘要" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if provider.calls != 2 {
		t.Fatalf("expected two model calls, got %d", provider.calls)
	}
	if probe.executions != 1 {
		t.Fatalf("expected one tool execution, got %d", probe.executions)
	}
}
