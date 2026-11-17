package main

import (
	"context"
	"log"
	"os"

	"github.com/sunqingliangai/go-tiny-claw/internal/engine"
	"github.com/sunqingliangai/go-tiny-claw/internal/provider"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

func main() {
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}

	workDir, _ := os.Getwd()

	// 使用 OpenAI 兼容协议连接真实的智谱模型。
	// 如需体验 Claude 兼容协议，只需替换为 provider.NewZhipuClaudeProvider。
	llmProvider := provider.NewZhipuOpenAIProvider("glm-4.5-air")
	registry := tools.NewRegistry()

	// 挂载极简工具集
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 开启慢思考，促使大模型一次性规划出并行的工具调用
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	prompt := `
	我当前目录下有 a.txt, b.txt, c.txt 三个文件。(如果没有请忽略找不到的报错)
	为了节省时间，请你同时一次性利用工具读取这三个文件，并将它们的内容综合起来告诉我。
	`

	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
