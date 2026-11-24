// internal/tools/bash.go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

type BashTool struct {
	workDir string // 工作区约束
}

func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "在当前工作区执行任意的 bash 命令。支持链式命令(如 &&)。返回标准输出(stdout)和标准错误(stderr)。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的 bash 命令，例如: ls -la 或 go test ./...",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command string `json:"command"`
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input bashArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", NewToolError(schema.ErrorCodeInvalidArguments, "参数解析失败", true, err)
	}

	// 【驾驭底线 1】：Time Budgeting (时间预算与超时控制)
	// 给予 bash 命令一个最大执行时间，防止大模型卡死进程 (比如运行了 top 或持续监听的 Web 服务)
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 在 macOS/Linux 下，我们通过将指令包裹在 `bash -c` 中执行，以支持环境变量、管道和逻辑与(&&)等复杂 Shell 语法。
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)

	// 【驾驭底线 2】：绑定执行的工作区目录
	// 确保命令默认在用户指定的 WorkDir 下执行，而不是引擎启动时的绝对路径。
	cmd.Dir = t.workDir

	// 执行并捕获 CombinedOutput (合并 stdout 和 stderr)
	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	// 错误输出也可能极大，因此在分类和回填 Context 之前统一截断。
	const maxLen = 8000
	if len(outputStr) > maxLen {
		outputStr = fmt.Sprintf("%s\n\n...[终端输出过长，已截断至前 %d 字节]...", outputStr[:maxLen], maxLen)
	}

	// 如果命令执行超时，返回警告信息让模型知晓
	if timeoutCtx.Err() == context.DeadlineExceeded {
		return "", &ToolError{
			Code:      schema.ErrorCodeBashTimeout,
			Message:   "命令执行超时(30s)，已被系统强制终止",
			Output:    outputStr,
			Retryable: false,
		}
	}
	if timeoutCtx.Err() == context.Canceled {
		return "", &ToolError{
			Code:      schema.ErrorCodeToolCanceled,
			Message:   "命令执行被取消",
			Output:    outputStr,
			Retryable: false,
			Err:       timeoutCtx.Err(),
		}
	}

	// 【驾驭底线 3】：结构化回传错误。
	// Registry 会把 ToolError 转成 IsError=true 的 ToolResult，Engine 继续循环让模型自纠，
	// 而不会把工具失败当成 Harness 致命错误直接退出。
	if err != nil {
		code := schema.ErrorCodeBashExitNonzero
		retryable := false

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 127 {
			code = schema.ErrorCodeBashCommandNotFound
		} else if strings.Contains(strings.ToLower(outputStr), "command not found") {
			code = schema.ErrorCodeBashCommandNotFound
		} else if errors.Is(err, exec.ErrNotFound) {
			code = schema.ErrorCodeBashStartFailed
		}

		return "", &ToolError{
			Code:      code,
			Message:   "命令执行失败",
			Output:    outputStr,
			Retryable: retryable,
			Err:       err,
		}
	}

	// 如果没有终端输出（比如仅仅执行了 mkdir），给模型一个明确的执行成功的反馈
	if outputStr == "" {
		return "命令执行成功，无终端输出。", nil
	}

	return outputStr, nil
}
