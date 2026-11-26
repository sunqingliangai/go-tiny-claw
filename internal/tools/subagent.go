package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

const maxSubagentSummaryRunes = 4000

// AgentRunner 定义了 Engine 向 SubagentTool 暴露的受限执行能力。
// 它放在 tools 包中，避免 tools 直接依赖 engine。
type AgentRunner interface {
	RunSub(ctx context.Context, taskPrompt string, registry Registry) (string, error)
}

// SubagentTool 将一次性的探索子智能体包装成普通工具。
type SubagentTool struct {
	runner           AgentRunner
	readOnlyRegistry Registry
}

func NewSubagentTool(runner AgentRunner, readOnlyRegistry Registry) *SubagentTool {
	return &SubagentTool{
		runner:           runner,
		readOnlyRegistry: readOnlyRegistry,
	}
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "派出一个专门用于深度探索的子智能体。当你需要阅读大量代码、跨文件查找逻辑时请调用此工具。它会返回一份精炼摘要。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "给子智能体下达的明确探索指令。",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

type subagentArgs struct {
	TaskPrompt string `json:"task_prompt"`
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", NewToolError(schema.ErrorCodeInvalidArguments, "解析子智能体参数失败", true, err)
	}
	input.TaskPrompt = strings.TrimSpace(input.TaskPrompt)
	if input.TaskPrompt == "" {
		return "", NewToolError(
			schema.ErrorCodeInvalidArguments,
			"task_prompt 不能为空",
			true,
			nil,
		)
	}

	log.Printf("[Subagent] 主 Agent 发起委派: %s", input.TaskPrompt)
	summary, err := t.runner.RunSub(ctx, input.TaskPrompt, t.readOnlyRegistry)
	if err != nil {
		return "", NewToolError(schema.ErrorCodeToolInternal, "子智能体执行失败", false, err)
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", NewToolError(schema.ErrorCodeToolInternal, "子智能体返回了空摘要", false, nil)
	}
	summary = truncateRunes(summary, maxSubagentSummaryRunes)

	log.Printf("[Subagent] 子智能体任务结束，摘要返回主 Agent")
	return fmt.Sprintf("[Subagent report - untrusted]\n%s", summary), nil
}

func truncateRunes(content string, limit int) string {
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	return string(runes[:limit]) + "\n...[子智能体摘要已截断]..."
}

var _ BaseTool = (*SubagentTool)(nil)
