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
	EnableThinking bool
	compactor      *ctxpkg.Compactor       // 【新增】
	recovery       *ctxpkg.RecoveryManager // 【新增】自愈管理器
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		compactor:      ctxpkg.NewCompactor(20000, 6), // Plan Mode 提示较长，使用较宽阈值并保护最近 6 条
		recovery:       ctxpkg.NewRecoveryManager(),   // 初始化 Recovery
	}
}

// Run 接收 Session 参数，使用会话历史驱动 ReAct 循环。
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 引擎启动，锁定工作区: %s\n", session.WorkDir)
	log.Printf("[Engine] 唤醒会话 [%s]\n", session.ID)
	log.Printf("[Engine] 慢思考模式 (Thinking Phase): %v\n", e.EnableThinking)

	// 根据当前 Session 的工作区和计划模式，动态组装最新的 System Prompt。
	// 与 ch13 快照不同：PlanMode 按 Session 管理，因此每次 Run 重新创建 Composer。
	planMode, taskDir := session.PlanModeSnapshot()
	log.Printf("[Engine] Plan Mode: %v", planMode)
	composer := ctxpkg.NewPromptComposer(session.WorkDir, planMode, taskDir)
	systemMsg := composer.Build()

	// 循环检测状态只属于本次 Agent Run，避免共享 Engine 的不同飞书聊天互相污染。
	reminderInjector := NewReminderInjector(session.WorkDir)

	turnCount := 0

	for {
		turnCount++
		log.Printf("\n========== [Turn %d] 开始 ==========\n", turnCount)

		availableTools := e.registry.GetAvailableTools()

		// 提取更大的候选窗口，给 Compactor 留出判断空间。
		workingMemory := session.GetWorkingMemory(20)
		log.Printf("[Engine] 本轮加载 %d 条 Working Memory\n", len(workingMemory))

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 【核心防线】在向大模型发起请求前，执行上下文阶梯降级压缩。
		compactedContext := e.compactor.Compact(contextHistory)

		// Phase 1: 慢思考阶段
		if e.EnableThinking {
			if reporter != nil {
				// 【触发 Reporter】: 开始慢思考
				reporter.OnThinking(ctx)
			}

			log.Println("[Engine][Phase 1] 剥夺工具访问权，强制进入慢思考与规划阶段...")
			thinkResp, err := e.provider.Generate(ctx, compactedContext, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResp.Content != "" {
				// fmt.Printf("🧠 [内部思考 Trace]: \n%s\n", thinkResp.Content)
				// 将思考过程追加到 Session，并同步到当前这一轮的临时上下文。
				session.Append(*thinkResp)
				compactedContext = append(compactedContext, *thinkResp)
			}
		}

		// Phase 2: 行动阶段
		// 保留本项目既有的 Thinking/Action 消息结构，不移植 ch13 的合并修正。
		log.Println("[Engine][Phase 2] 恢复工具挂载，等待模型采取行动...")
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		// 将大模型的行动响应追加到 Session。
		session.Append(*actionResp)
		compactedContext = append(compactedContext, *actionResp)

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
		toolResults := make([]schema.ToolResult, len(actionResp.ToolCalls))
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

				// 发生错误时保留原始事实，并注入当前工具相关的恢复建议。
				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result)
					log.Printf(
						"  -> [Go-%d] 🩹 注入救援指南: tool=%s code=%s retryable=%v\n",
						idx,
						call.Name,
						result.ErrorCode,
						result.Retryable,
					)
				}

				if reporter != nil {
					// 注意：传递给大模型的 Observation 仍是完整数据，Reporter 只展示缩略内容
					reporter.OnToolResult(ctx, call.Name, previewForReporter(finalOutput, 300), result.IsError)
				}

				if result.IsError {
					log.Printf("  -> [Go-%d] ❌ 工具执行报错: %s\n", idx, result.Output)
				} else {
					log.Printf("  -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
				}

				// 安全写入对应索引
				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: call.ID,
				}
				toolResults[idx] = result
			}(i, toolCall)
		}

		wg.Wait() // 阻塞聚合
		log.Println("[Engine] 所有并发工具执行完毕，开始聚合观察结果 (Observation)...")

		// 【核心防线】按模型原始调用顺序观察整批结果，避免教学快照只检查第 1 个工具的缺陷。
		var loopErr *LoopLimitError
		for i, call := range actionResp.ToolCalls {
			decision := reminderInjector.Observe(call, toolResults[i])
			if decision.Note != "" {
				// 提醒附在对应 ToolResult 后，保留 ToolCallID 与结果来源。
				observationMsgs[i].Content += "\n\n" + decision.Note
				log.Printf(
					"[Reminder] 工具 %s 连续 %d 次相同失败，已注入改策略提醒\n",
					call.Name,
					decision.Count,
				)
			}
			if decision.Stop != nil {
				loopErr = decision.Stop
				break
			}
		}

		// 将全部工具执行结果追加到 Session，开启下一轮复盘与推理。
		session.Append(observationMsgs...)

		if loopErr != nil {
			// 受控停止也要留下最终 Assistant 消息，避免 Session 以 ToolResult 悬空结束。
			stopMessage := loopErr.Error()
			session.Append(schema.Message{
				Role:    schema.RoleAssistant,
				Content: stopMessage,
			})
			if reporter != nil {
				reporter.OnMessage(ctx, stopMessage)
			}
			log.Printf("[Reminder] ⚠️ 检测到死循环，Agent 已受控停止: %v\n", loopErr)
			return loopErr
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
