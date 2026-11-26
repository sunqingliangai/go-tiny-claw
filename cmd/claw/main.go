package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sunqingliangai/go-tiny-claw/internal/engine"
	"github.com/sunqingliangai/go-tiny-claw/internal/feishu"
	"github.com/sunqingliangai/go-tiny-claw/internal/provider"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

func main() {
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}
	if os.Getenv("FEISHU_APP_ID") == "" || os.Getenv("FEISHU_APP_SECRET") == "" {
		log.Fatal("请先导出 FEISHU_APP_ID 和 FEISHU_APP_SECRET")
	}

	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("获取工作目录失败: %v", err)
	}

	// 使用 OpenAI 兼容协议连接真实的智谱模型。
	// 如需体验 Claude 兼容协议，只需替换为 provider.NewZhipuClaudeProvider。
	llmProvider := provider.NewZhipuOpenAIProvider("glm-4.5-air")

	// 为子智能体准备课程同款探索工具集。
	// 为保持第 17 讲代码简单，该 Registry 不挂审批 Middleware；Bash 仍拥有宿主机权限，仅适合教学演示。
	exploreRegistry := tools.NewRegistry()
	exploreRegistry.Register(tools.NewReadFileTool(workDir))
	exploreRegistry.Register(tools.NewBashTool(workDir))

	registry := tools.NewRegistry()

	// 挂载极简工具集
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 本章默认策略：工作区内文件工具放行，Bash 必须经过飞书人工审批。
	registry.Use(tools.NewDefaultPolicy(workDir))

	// 开启慢思考
	eng := engine.NewAgentEngine(llmProvider, registry, false)

	// Subagent 对主 Agent 暴露为普通工具；子 Registry 刻意不包含 spawn_subagent，避免递归委派。
	registry.Register(tools.NewSubagentTool(eng, exploreRegistry))

	bot, err := feishu.NewBot(eng, feishu.Config{
		AppID:       os.Getenv("FEISHU_APP_ID"),
		AppSecret:   os.Getenv("FEISHU_APP_SECRET"),
		WorkDir:     workDir,
		QueueSize:   8,
		TaskTimeout: 10 * time.Minute,
		ApproverIDs: strings.Split(os.Getenv("FEISHU_APPROVER_IDS"), ","),
	})
	if err != nil {
		log.Fatalf("初始化飞书机器人失败: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("正在启动飞书 WebSocket 长连接...")
	if err := bot.Start(ctx); err != nil {
		log.Fatalf("飞书长连接退出: %v", err)
	}
}
