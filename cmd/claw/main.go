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
	registry.Register(tools.NewEditFileTool(workDir)) // 挂载 Edit 工具

	// 开启慢思考模式
	// 注：教学快照此处实际传入 false，为保持行为一致，Thinking 仍关闭。
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 发起一个需要局部修改的任务
	prompt := `
	我当前目录下有一个 server.go 文件。
	请帮我把里面 "TODO: 增加鉴权逻辑" 下面的那个 if 语句，整个替换为：
	if user == nil {
		fmt.Println("Forbidden!")
		return
	}
	`

	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
