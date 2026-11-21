package main

import (
	"context"
	"log"
	"os"
	"os/signal"
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
	registry := tools.NewRegistry()

	// 挂载极简工具集
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 开启慢思考
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	bot, err := feishu.NewBot(eng, feishu.Config{
		AppID:       os.Getenv("FEISHU_APP_ID"),
		AppSecret:   os.Getenv("FEISHU_APP_SECRET"),
		QueueSize:   8,
		TaskTimeout: 10 * time.Minute,
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
