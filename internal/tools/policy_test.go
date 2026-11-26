package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

func TestDefaultPolicyRequiresApprovalForBash(t *testing.T) {
	policy := NewDefaultPolicy(t.TempDir())
	decision, err := policy(context.Background(), schema.ToolCall{
		Name:      "bash",
		Arguments: json.RawMessage(`{"command":"ls -la"}`),
	})

	if err != nil {
		t.Fatalf("policy failed: %v", err)
	}
	if decision.Effect != PolicyAsk {
		t.Fatalf("bash should require approval, got %s", decision.Effect)
	}
}

func TestDefaultPolicyAllowsSubagentSpawn(t *testing.T) {
	policy := NewDefaultPolicy(t.TempDir())
	decision, err := policy(context.Background(), schema.ToolCall{
		Name:      "spawn_subagent",
		Arguments: json.RawMessage(`{"task_prompt":"探索代码"}`),
	})

	if err != nil {
		t.Fatalf("policy failed: %v", err)
	}
	if decision.Effect != PolicyAllow {
		t.Fatalf("subagent spawn should be allowed, got %s", decision.Effect)
	}
}

func TestDefaultPolicyAllowsFilesInsideWorkspace(t *testing.T) {
	workDir := t.TempDir()
	policy := NewDefaultPolicy(workDir)

	for _, toolName := range []string{"read_file", "write_file", "edit_file"} {
		t.Run(toolName, func(t *testing.T) {
			decision, err := policy(context.Background(), schema.ToolCall{
				Name:      toolName,
				Arguments: json.RawMessage(`{"path":"nested/file.txt"}`),
			})
			if err != nil {
				t.Fatalf("policy failed: %v", err)
			}
			if decision.Effect != PolicyAllow {
				t.Fatalf("workspace file should be allowed, got %s", decision.Effect)
			}
		})
	}
}

func TestDefaultPolicyDeniesFilesOutsideWorkspace(t *testing.T) {
	workDir := t.TempDir()
	policy := NewDefaultPolicy(workDir)
	outside := filepath.Join(filepath.Dir(workDir), "outside.txt")

	decision, err := policy(context.Background(), schema.ToolCall{
		Name:      "read_file",
		Arguments: json.RawMessage(`{"path":` + mustJSONStringForPolicy(t, outside) + `}`),
	})

	if err != nil {
		t.Fatalf("policy failed: %v", err)
	}
	if decision.Effect != PolicyDeny || decision.ErrorCode != schema.ErrorCodePolicyDenied {
		t.Fatalf("outside path should be denied: %#v", decision)
	}
}

func TestDefaultPolicyDeniesMalformedFileArguments(t *testing.T) {
	policy := NewDefaultPolicy(t.TempDir())
	decision, err := policy(context.Background(), schema.ToolCall{
		Name:      "read_file",
		Arguments: json.RawMessage(`{"path":`),
	})

	if err != nil {
		t.Fatalf("policy failed: %v", err)
	}
	if decision.Effect != PolicyDeny || decision.ErrorCode != schema.ErrorCodeInvalidArguments {
		t.Fatalf("malformed arguments should be denied: %#v", decision)
	}
}

func mustJSONStringForPolicy(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal path: %v", err)
	}
	return string(encoded)
}
