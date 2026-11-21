package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	ctxpkg "github.com/sunqingliangai/go-tiny-claw/internal/context"
	"github.com/sunqingliangai/go-tiny-claw/internal/provider"
	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	WorkDir        string
	EnableThinking bool
	composer       *ctxpkg.PromptComposer // 【新增】
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		composer:       ctxpkg.NewPromptComposer(workDir), // 【初始化】
	}
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	log.Printf("[Engine] 引擎启动，锁定工作区: %s\n", e.WorkDir)
	log.Printf("[Engine] 慢思考模式 (Thinking Phase): %v\n", e.EnableThinking)

	// 【核心修改】动态组装 System Prompt
	systemMsg := e.composer.Build()

	contextHistory := []schema.Message{
		systemMsg,
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0

	for {
		turnCount++
		log.Printf("\n========== [Turn %d] 开始 ==========\n", turnCount)

		availableTools := e.registry.GetAvailableTools()

		// Phase 1: 慢思考阶段
		if e.EnableThinking {
			if reporter != nil {
				// 【触发 Reporter】: 开始慢思考
				reporter.OnThinking(ctx)
			}

			log.Println("[Engine][Phase 1] 剥夺工具访问权，强制进入慢思考与规划阶段...")
			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResp.Content != "" {
				// fmt.Printf("🧠 [内部思考 Trace]: \n%s\n", thinkResp.Content)
				contextHistory = append(contextHistory, *thinkResp)
			}
		}

		// Phase 2: 行动阶段
		log.Println("[Engine][Phase 2] 恢复工具挂载，等待模型采取行动...")
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		// if actionResp.Content != "" {
		// 	fmt.Printf("🤖 [对外回复]: \n%s\n", actionResp.Content)
		// }

		if len(actionResp.ToolCalls) == 0 {
			if actionResp.Content != "" && reporter != nil {
				// 【触发 Reporter】: 只把没有后续 Tool Call 的内容作为最终回复
				reporter.OnMessage(ctx, actionResp.Content)
			}
			log.Println("[Engine] 模型回复中不含tool_call，任务宣告完成。")
			break
		}

		log.Printf("[Engine] 模型请求并发调用 %d 个工具...\n", len(actionResp.ToolCalls))

		// ================= 并发执行逻辑 =================

		// 预分配切片以保证顺序并避免并发写入锁
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					// 【触发 Reporter】: 报告即将在底层执行的工具
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				log.Printf("  -> [Go-%d] 🛠️ 触发并行执行: %s\n", idx, call.Name)

				// 执行底层工具
				result := e.registry.Execute(ctx, call)

				if reporter != nil {
					// 注意：传递给大模型的 Observation 仍是完整数据，Reporter 只展示缩略内容
					reporter.OnToolResult(ctx, call.Name, previewForReporter(result.Output, 300), result.IsError)
				}

				if result.IsError {
					log.Printf("  -> [Go-%d] ❌ 工具执行报错: %s\n", idx, result.Output)
				} else {
					log.Printf("  -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
				}

				// 安全写入对应索引
				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}

		wg.Wait() // 阻塞聚合
		log.Println("[Engine] 所有并发工具执行完毕，开始聚合观察结果 (Observation)...")

		// 按序追加回 Context
		for _, obs := range observationMsgs {
			contextHistory = append(contextHistory, obs)
		}
	}

	return nil
}

func previewForReporter(content string, limit int) string {
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	return string(runes[:limit]) + "... (已截断)"
}
