package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

const (
	// 第 3 次连续相同失败只进行软干预，给模型改变策略的机会。
	reminderWarnThreshold = 3
	// 第 8 次连续相同失败停止进入下一轮模型调用，避免提醒被持续无视。
	reminderStopThreshold = 8
)

// LoopLimitError 表示 Agent 因连续执行相同的失败动作而被受控终止。
// 它不是工具执行错误，也不会由 Tool Registry 回填给模型。
type LoopLimitError struct {
	ToolName  string
	Count     int
	ErrorCode schema.ErrorCode
}

func (e *LoopLimitError) Error() string {
	errorCode := string(e.ErrorCode)
	if errorCode == "" {
		errorCode = "未知错误"
	}
	return fmt.Sprintf(
		"Agent 已停止：工具 %q 连续 %d 次使用相同参数调用失败，最近错误码为 %s。请调整任务或发送新的消息后再试。",
		e.ToolName,
		e.Count,
		errorCode,
	)
}

// ReminderDecision 是单次工具结果触发的一次循环保护决策。
// Note 非空时由 Main Loop 附在对应 ToolResult 后，Stop 非空时结束本次 Agent Run。
type ReminderDecision struct {
	Note  string
	Stop  *LoopLimitError
	Count int
}

// ReminderInjector 在单次 Agent Run 内维护连续相同失败动作的状态。
// 它必须在每次 Run 时重新创建，避免不同飞书聊天互相影响。
type ReminderInjector struct {
	workDir string

	currentKey   string
	failureCount int
	reminded     bool
}

func NewReminderInjector(workDir string) *ReminderInjector {
	return &ReminderInjector{workDir: workDir}
}

// Observe 分析一个工具执行结果，并返回提醒或停止决策。
// 本项目有意偏离教学快照：只对连续失败计数，任意成功或不同动作都会重置。
func (r *ReminderInjector) Observe(call schema.ToolCall, result schema.ToolResult) ReminderDecision {
	if !result.IsError {
		r.reset()
		return ReminderDecision{}
	}

	key := canonicalToolCallKey(r.workDir, call)
	if key != r.currentKey {
		r.currentKey = key
		r.failureCount = 1
		r.reminded = false
	} else {
		r.failureCount++
	}

	decision := ReminderDecision{Count: r.failureCount}
	if r.failureCount >= reminderStopThreshold {
		decision.Stop = &LoopLimitError{
			ToolName:  call.Name,
			Count:     r.failureCount,
			ErrorCode: result.ErrorCode,
		}
		return decision
	}
	if r.failureCount >= reminderWarnThreshold && !r.reminded {
		r.reminded = true
		decision.Note = formatLoopReminder(call.Name, r.failureCount, result.ErrorCode)
	}
	return decision
}

func (r *ReminderInjector) reset() {
	r.currentKey = ""
	r.failureCount = 0
	r.reminded = false
}

func formatLoopReminder(toolName string, count int, errorCode schema.ErrorCode) string {
	code := string(errorCode)
	if code == "" {
		code = "未知错误"
	}
	return fmt.Sprintf(`[SYSTEM REMINDER 预警]
你已连续 %d 次使用规范化后相同的参数调用 %q 工具，并且每次都失败（最近错误码：%s）。
请不要继续原样重试。先重新读取相关文件或检查当前状态，再更换参数、工具或实现策略。
如果现有工具无法解决问题，请停止猜测并向用户说明需要哪些人工帮助。
连续第 %d 次相同失败时，本次 Agent 运行会被强制停止。`,
		count,
		toolName,
		code,
		reminderStopThreshold,
	)
}

// canonicalToolCallKey 生成工具名与规范化参数的稳定指纹。
// 参数非法时退回原始文本，保证相同的坏参数仍能被识别为重复动作。
func canonicalToolCallKey(workDir string, call schema.ToolCall) string {
	canonical := canonicalizeArguments(workDir, call.Arguments)
	sum := sha256.Sum256([]byte(call.Name + "\x00" + canonical))
	return fmt.Sprintf("%x", sum)
}

func canonicalizeArguments(workDir string, args []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return strings.TrimSpace(string(args))
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return strings.TrimSpace(string(args))
	}

	normalized := normalizeJSONValue(workDir, "", value)
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return strings.TrimSpace(string(args))
	}
	return string(canonical)
}

func normalizeJSONValue(workDir, key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for field := range typed {
			keys = append(keys, field)
		}
		sort.Strings(keys)

		normalized := make(map[string]any, len(typed))
		for _, field := range keys {
			normalized[field] = normalizeJSONValue(workDir, field, typed[field])
		}
		return normalized

	case []any:
		normalized := make([]any, len(typed))
		for i, entry := range typed {
			// 数组顺序具有语义，不能排序或去重。
			normalized[i] = normalizeJSONValue(workDir, key, entry)
		}
		return normalized

	case string:
		switch key {
		case "path":
			return normalizePathArgument(workDir, typed)
		case "command":
			// Shell 命令内部空白和引号可能改变语义，因此只去掉外层空白。
			return strings.TrimSpace(typed)
		default:
			return typed
		}

	default:
		return value
	}
}

func normalizePathArgument(workDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(workDir, path))
}
