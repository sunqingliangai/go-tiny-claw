package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

type pathArguments struct {
	Path string `json:"path"`
}

// NewDefaultPolicy 返回本章的默认执行前策略：
// 工作区内文件工具放行，Bash 全部进入人工审批，工作区外路径拒绝。
func NewDefaultPolicy(workDir string) MiddlewareFunc {
	cleanWorkDir := filepath.Clean(workDir)
	if absolute, err := filepath.Abs(cleanWorkDir); err == nil {
		cleanWorkDir = absolute
	}

	return func(_ context.Context, call schema.ToolCall) (MiddlewareDecision, error) {
		switch call.Name {
		case "read_file", "write_file", "edit_file":
			var args pathArguments
			if err := json.Unmarshal(call.Arguments, &args); err != nil || strings.TrimSpace(args.Path) == "" {
				return MiddlewareDecision{
					Effect:    PolicyDeny,
					Reason:    "文件工具参数不合法，无法确认目标路径。",
					ErrorCode: schema.ErrorCodeInvalidArguments,
				}, nil
			}

			fullPath, inside, err := resolvePathInsideWorkspace(cleanWorkDir, args.Path)
			if err != nil {
				return MiddlewareDecision{
					Effect:    PolicyDeny,
					Reason:    fmt.Sprintf("无法解析目标路径: %v", err),
					ErrorCode: schema.ErrorCodePolicyDenied,
				}, nil
			}
			if !inside {
				return MiddlewareDecision{
					Effect:    PolicyDeny,
					Reason:    fmt.Sprintf("路径 %q 超出工作区，已拒绝执行。", fullPath),
					ErrorCode: schema.ErrorCodePolicyDenied,
				}, nil
			}
			return MiddlewareDecision{Effect: PolicyAllow}, nil

		case "bash":
			return MiddlewareDecision{
				Effect: PolicyAsk,
				Reason: "Bash 命令拥有宿主机文件、进程和网络权限，需要人工审批。",
			}, nil

		case "spawn_subagent":
			// 子 Agent 使用独立 Registry；委派本身不直接产生写副作用。
			return MiddlewareDecision{Effect: PolicyAllow}, nil

		default:
			return MiddlewareDecision{
				Effect: PolicyAsk,
				Reason: fmt.Sprintf("工具 %q 没有明确的内置策略，默认要求人工审批。", call.Name),
			}, nil
		}
	}
}

func resolvePathInsideWorkspace(workDir, target string) (string, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false, fmt.Errorf("路径为空")
	}

	fullPath := filepath.Clean(target)
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(workDir, fullPath)
	}

	relative, err := filepath.Rel(workDir, fullPath)
	if err != nil {
		return fullPath, false, err
	}
	outside := relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
	return fullPath, !outside, nil
}
