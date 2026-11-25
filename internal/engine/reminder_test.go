package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/sunqingliangai/go-tiny-claw/internal/context"
	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

func TestCanonicalToolCallKeyNormalizesJSONAndPaths(t *testing.T) {
	workDir := "/tmp/tiny-claw-work"

	first := schema.ToolCall{
		Name: "read_file",
		Arguments: json.RawMessage(
			`{"path":" a.txt ","options":{"line":2,"format":"text"}}`,
		),
	}
	second := schema.ToolCall{
		Name: "read_file",
		Arguments: json.RawMessage(
			`{"options":{"format":"text","line":2},"path":"./a.txt"}`,
		),
	}
	third := schema.ToolCall{
		Name: "read_file",
		Arguments: json.RawMessage(
			`{"path":"sub/../a.txt","options":{"line":2,"format":"text"}}`,
		),
	}

	firstKey := canonicalToolCallKey(workDir, first)
	if firstKey != canonicalToolCallKey(workDir, second) {
		t.Fatal("JSON key order or path alias changed the fingerprint")
	}
	if firstKey != canonicalToolCallKey(workDir, third) {
		t.Fatal("cleaned relative path changed the fingerprint")
	}
}

func TestCanonicalToolCallKeyKeepsSemanticDifferences(t *testing.T) {
	workDir := "/tmp/tiny-claw-work"

	bashCall := func(command string) schema.ToolCall {
		return schema.ToolCall{
			Name:      "bash",
			Arguments: json.RawMessage(`{"command":` + mustJSONString(t, command) + `}`),
		}
	}

	if canonicalToolCallKey(workDir, bashCall(" ls -la ")) != canonicalToolCallKey(workDir, bashCall("ls -la")) {
		t.Fatal("outer command whitespace should be normalized")
	}
	if canonicalToolCallKey(workDir, bashCall("ls -la")) == canonicalToolCallKey(workDir, bashCall("ls  -la")) {
		t.Fatal("semantic shell whitespace must not be normalized")
	}

	pathA := schema.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}
	pathB := schema.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"b.txt"}`)}
	if canonicalToolCallKey(workDir, pathA) == canonicalToolCallKey(workDir, pathB) {
		t.Fatal("different file paths must have different fingerprints")
	}
}

func TestCanonicalToolCallKeyPreservesArrayOrder(t *testing.T) {
	first := schema.ToolCall{
		Name:      "batch_tool",
		Arguments: json.RawMessage(`{"items":[1,2,3]}`),
	}
	second := schema.ToolCall{
		Name:      "batch_tool",
		Arguments: json.RawMessage(`{"items":[3,2,1]}`),
	}

	if canonicalToolCallKey("/tmp", first) == canonicalToolCallKey("/tmp", second) {
		t.Fatal("array order must remain part of the action identity")
	}
}

func TestReminderInjectorWarnsOnceAndStopsAtLimit(t *testing.T) {
	injector := NewReminderInjector("/tmp/tiny-claw-work")
	call := schema.ToolCall{
		Name:      "read_file",
		Arguments: json.RawMessage(`{"path":"a.txt"}`),
	}
	result := schema.ToolResult{
		IsError:   true,
		ErrorCode: schema.ErrorCodeFileNotFound,
	}

	var noteCount int
	for count := 1; count <= reminderStopThreshold; count++ {
		decision := injector.Observe(call, result)
		if decision.Count != count {
			t.Fatalf("unexpected count: got %d want %d", decision.Count, count)
		}
		if decision.Note != "" {
			noteCount++
			if count != reminderWarnThreshold {
				t.Fatalf("note injected at unexpected count: %d", count)
			}
			if !strings.Contains(decision.Note, "SYSTEM REMINDER") {
				t.Fatalf("reminder marker missing: %q", decision.Note)
			}
		}
		if count < reminderStopThreshold && decision.Stop != nil {
			t.Fatalf("stopped before threshold: %d", count)
		}
		if count == reminderStopThreshold {
			if decision.Stop == nil {
				t.Fatal("expected loop stop at threshold")
			}
			if decision.Stop.Count != reminderStopThreshold {
				t.Fatalf("unexpected stop count: %d", decision.Stop.Count)
			}
		}
	}
	if noteCount != 1 {
		t.Fatalf("soft reminder should be injected once, got %d", noteCount)
	}
}

func TestReminderInjectorResetsOnSuccessOrDifferentAction(t *testing.T) {
	injector := NewReminderInjector("/tmp/tiny-claw-work")
	failing := schema.ToolResult{
		IsError:   true,
		ErrorCode: schema.ErrorCodeFileNotFound,
	}

	callA := schema.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}
	callB := schema.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"b.txt"}`)}

	injector.Observe(callA, failing)
	injector.Observe(callA, failing)
	decision := injector.Observe(callA, schema.ToolResult{IsError: false})
	if decision.Count != 0 {
		t.Fatalf("success should reset the loop state, got count %d", decision.Count)
	}

	injector.Observe(callA, failing)
	injector.Observe(callA, failing)
	decision = injector.Observe(callB, failing)
	if decision.Count != 1 {
		t.Fatalf("different action should start a new streak, got count %d", decision.Count)
	}
}

func TestReminderStateIsScopedToInjectorInstance(t *testing.T) {
	call := schema.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}
	result := schema.ToolResult{
		IsError:   true,
		ErrorCode: schema.ErrorCodeFileNotFound,
	}

	firstRun := NewReminderInjector("/tmp/tiny-claw-work")
	secondRun := NewReminderInjector("/tmp/tiny-claw-work")

	firstRun.Observe(call, result)
	firstRun.Observe(call, result)
	decision := secondRun.Observe(call, result)
	if decision.Count != 1 {
		t.Fatalf("a new Agent Run must not inherit another run's count, got %d", decision.Count)
	}
}

func TestCanonicalToolCallKeyFallsBackForMalformedArguments(t *testing.T) {
	call := schema.ToolCall{
		Name:      "read_file",
		Arguments: json.RawMessage(`{"path":`),
	}
	if canonicalToolCallKey("/tmp", call) == "" {
		t.Fatal("malformed arguments should still produce a stable fallback key")
	}
}

func TestAgentEngineRemindsThenStopsRepeatedFailureLoop(t *testing.T) {
	provider := &loopingProvider{}
	reporter := &recordingReporter{}
	engine := NewAgentEngine(provider, alwaysFailRegistry{}, false)

	workDir := t.TempDir()
	session := ctxpkg.NewSession("test-chat", workDir)
	session.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: "请读取一个不存在的文件，并在失败时原样重试。",
	})

	err := engine.Run(context.Background(), session, reporter)
	var loopErr *LoopLimitError
	if !errors.As(err, &loopErr) {
		t.Fatalf("expected LoopLimitError, got %v", err)
	}
	if len(provider.requests) != reminderStopThreshold {
		t.Fatalf(
			"model should be called %d times, got %d",
			reminderStopThreshold,
			len(provider.requests),
		)
	}

	// 第 3 次失败产生提醒，第 4 次模型请求必须能看到它。
	if !containsMessageText(provider.requests[reminderWarnThreshold], "SYSTEM REMINDER") {
		t.Fatal("soft reminder was not visible on the next model request")
	}
	if len(reporter.messages) != 1 || !strings.Contains(reporter.messages[0], "Agent 已停止") {
		t.Fatalf("controlled stop message missing: %#v", reporter.messages)
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test string: %v", err)
	}
	return string(encoded)
}

type loopingProvider struct {
	requests [][]schema.Message
}

func (p *loopingProvider) Generate(
	_ context.Context,
	messages []schema.Message,
	_ []schema.ToolDefinition,
) (*schema.Message, error) {
	requestCopy := make([]schema.Message, len(messages))
	copy(requestCopy, messages)
	p.requests = append(p.requests, requestCopy)

	path := " a.txt "
	switch len(p.requests) {
	case 2:
		path = "./a.txt"
	case 3:
		path = "sub/../a.txt"
	}
	arguments, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return nil, fmt.Errorf("构造测试参数失败: %w", err)
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{
				ID:        fmt.Sprintf("call-%d", len(p.requests)),
				Name:      "read_file",
				Arguments: arguments,
			},
		},
	}, nil
}

type alwaysFailRegistry struct{}

func (alwaysFailRegistry) Register(tools.BaseTool) {}

func (alwaysFailRegistry) Use(tools.MiddlewareFunc) {}

func (alwaysFailRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{{Name: "read_file"}}
}

func (alwaysFailRegistry) Execute(_ context.Context, call schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "文件不存在",
		IsError:    true,
		ErrorCode:  schema.ErrorCodeFileNotFound,
		Retryable:  true,
	}
}

type recordingReporter struct {
	messages []string
}

func (*recordingReporter) OnThinking(context.Context) {}

func (*recordingReporter) OnToolCall(context.Context, string, string) {}

func (*recordingReporter) OnToolResult(context.Context, string, string, bool) {}

func (r *recordingReporter) OnMessage(_ context.Context, content string) {
	r.messages = append(r.messages, content)
}

func containsMessageText(messages []schema.Message, text string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}
