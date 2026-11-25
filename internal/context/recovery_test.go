package context

import (
	"strings"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

func TestRecoveryManagerUsesStructuredErrorCode(t *testing.T) {
	manager := NewRecoveryManager()

	tests := []struct {
		name     string
		code     schema.ErrorCode
		wantHint string
	}{
		{
			name:     "file not found",
			code:     schema.ErrorCodeFileNotFound,
			wantHint: "ls 或 find",
		},
		{
			name:     "edit mismatch",
			code:     schema.ErrorCodeEditOldTextNotFound,
			wantHint: "read_file",
		},
		{
			name:     "bash timeout",
			code:     schema.ErrorCodeBashTimeout,
			wantHint: "缩小执行范围",
		},
		{
			name:     "policy denied",
			code:     schema.ErrorCodePolicyDenied,
			wantHint: "不要尝试换写法绕过",
		},
		{
			name:     "approval timeout",
			code:     schema.ErrorCodeApprovalTimeout,
			wantHint: "不要自动重试",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := manager.AnalyzeAndInject("tool", schema.ToolResult{
				Output:    "原始错误",
				IsError:   true,
				ErrorCode: test.code,
			})

			if !strings.Contains(result, "原始错误") {
				t.Fatalf("raw error missing: %q", result)
			}
			if !strings.Contains(result, "[系统救援指南]") || !strings.Contains(result, test.wantHint) {
				t.Fatalf("recovery hint missing: %q", result)
			}
		})
	}
}

func TestRecoveryManagerLeavesUnknownErrorUnchanged(t *testing.T) {
	manager := NewRecoveryManager()
	result := manager.AnalyzeAndInject("unknown_tool", schema.ToolResult{
		Output:  "unknown failure",
		IsError: true,
	})

	if result != "unknown failure" {
		t.Fatalf("unknown error should remain unchanged: %q", result)
	}
}
