package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

// ToolError 是工具层和 Harness 层之间的结构化错误协议。
type ToolError struct {
	Code      schema.ErrorCode
	Message   string
	Output    string
	Retryable bool
	Err       error
}

func NewToolError(code schema.ErrorCode, message string, retryable bool, cause error) *ToolError {
	return &ToolError{
		Code:      code,
		Message:   message,
		Retryable: retryable,
		Err:       cause,
	}
}

func (e *ToolError) Error() string {
	if e.Message != "" && e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "工具执行失败"
}

func (e *ToolError) Unwrap() error {
	return e.Err
}

// ModelOutput 为模型保留错误原因、工具输出以及稳定的错误码。
func (e *ToolError) ModelOutput(toolName string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Error executing %s [%s]: %s", toolName, e.Code, e.Error())
	if strings.TrimSpace(e.Output) != "" {
		builder.WriteString("\n输出:\n")
		builder.WriteString(e.Output)
	}
	return builder.String()
}

func classifyFileError(operation string, err error) *ToolError {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return NewToolError(schema.ErrorCodeFileNotFound, operation, true, err)
	case errors.Is(err, fs.ErrPermission):
		return NewToolError(schema.ErrorCodePermissionDenied, operation, false, err)
	default:
		return NewToolError(schema.ErrorCodeToolInternal, operation, false, err)
	}
}
