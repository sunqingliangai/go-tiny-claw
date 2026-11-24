package schema

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ErrorCode string

const (
	ErrorCodeToolNotFound         ErrorCode = "TOOL_NOT_FOUND"
	ErrorCodeToolInternal         ErrorCode = "TOOL_INTERNAL"
	ErrorCodeToolCanceled         ErrorCode = "TOOL_CANCELED"
	ErrorCodeInvalidArguments     ErrorCode = "TOOL_ARGUMENT_INVALID"
	ErrorCodeFileNotFound         ErrorCode = "FILE_NOT_FOUND"
	ErrorCodePermissionDenied     ErrorCode = "PERMISSION_DENIED"
	ErrorCodeEditOldTextNotFound  ErrorCode = "EDIT_OLD_TEXT_NOT_FOUND"
	ErrorCodeEditOldTextAmbiguous ErrorCode = "EDIT_OLD_TEXT_AMBIGUOUS"
	ErrorCodeBashExitNonzero      ErrorCode = "BASH_EXIT_NONZERO"
	ErrorCodeBashTimeout          ErrorCode = "BASH_TIMEOUT"
	ErrorCodeBashCommandNotFound  ErrorCode = "BASH_COMMAND_NOT_FOUND"
	ErrorCodeBashStartFailed      ErrorCode = "BASH_START_FAILED"
)

type ToolResult struct {
	ToolCallID string    `json:"tool_call_id"`
	Output     string    `json:"output"`
	IsError    bool      `json:"is_error"`
	ErrorCode  ErrorCode `json:"error_code,omitempty"`
	Retryable  bool      `json:"retryable,omitempty"`
}

type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}
