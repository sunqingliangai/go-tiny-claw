// internal/context/compactor.go
package context

import (
	"fmt"
	"log"
	"unicode/utf8"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

// Compactor 负责监控和压缩上下文内存，防止大模型发生上下文超限。
type Compactor struct {
	// MaxChars 保留教学快照的字段名；当前实现实际按 UTF-8 字节数估算。
	MaxChars       int
	RetainLastMsgs int // Working Memory 保护区：最近的 N 条消息
}

func NewCompactor(maxChars int, retainLastMsgs int) *Compactor {
	return &Compactor{
		MaxChars:       maxChars,
		RetainLastMsgs: retainLastMsgs,
	}
}

// Compact 接收准备发送给大模型的消息数组。
// 如果总长度超标，对远期历史区进行全量掩码 (Masking)，对短期保护区进行超长局部截断 (Truncation)。
func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	currentLength := c.estimateLength(msgs)

	// 如果没有超过水位线，直接返回原数组 (大多数情况下的正常路径)
	if currentLength < c.MaxChars {
		return msgs
	}

	log.Printf("[Compactor] ⚠️ 上下文告警：当前长度 (%d 字节) 超过阈值 (%d)，触发压缩清理...\n", currentLength, c.MaxChars)

	var compacted []schema.Message
	msgCount := len(msgs)

	// 计算受保护的 Working Memory 起始索引
	protectStartIndex := msgCount - c.RetainLastMsgs
	if protectStartIndex < 0 {
		protectStartIndex = 0
	}

	for i, msg := range msgs {
		// System Prompt 绝对不能动，直接保留。
		if msg.Role == schema.RoleSystem {
			compacted = append(compacted, msg)
			continue
		}

		// 拷贝一份新消息，避免压缩过程污染 Session 中的原始历史。
		newMsg := msg

		isInWorkingMemory := i >= protectStartIndex

		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			// 对于工具的返回结果 (Observation/ToolResult)
			if !isInWorkingMemory {
				// 【第一道防线：远期历史】执行无情替换 (Full Masking)
				if len(msg.Content) > 200 {
					newMsg.Content = fmt.Sprintf("...[为了节省内存，早期的工具输出已被系统强制清理。原始长度: %d 字节]...", len(msg.Content))
				}
			} else {
				// 【第二道防线：短期记忆】即使处于近期保护区，只要单条内容过大，也必须截断防超限。
				// 教学代码按字节截取，这里增加 UTF-8 边界保护，避免切断中文字符产生非法字符串。
				const maxKeep = 1000
				if len(msg.Content) > maxKeep {
					head := utf8SafePrefix(msg.Content, 500)
					tail := utf8SafeSuffix(msg.Content, 500)
					removedBytes := len(msg.Content) - len(head) - len(tail)
					newMsg.Content = fmt.Sprintf("%s\n\n...[内容过长，中间 %d 字节已被系统截断]...\n\n%s", head, removedBytes, tail)
				}
			}
		} else if msg.Role == schema.RoleAssistant && msg.Content != "" {
			// 对于大模型的冗长思考或旧回复进行折叠。
			if !isInWorkingMemory && len(msg.Content) > 200 {
				newMsg.Content = "...[早期的推理思考过程已折叠]..."
			}
		}

		// 绝不修改 ToolCalls，因为这是模型行动意图和 ReAct 逻辑链的证据。
		compacted = append(compacted, newMsg)
	}

	newLength := c.estimateLength(compacted)
	log.Printf("[Compactor] ✅ 压缩完成。上下文长度从 %d 降至 %d 字节。\n", currentLength, newLength)

	return compacted
}

// estimateLength 粗略计算当前上下文的总 UTF-8 字节长度。
func (c *Compactor) estimateLength(msgs []schema.Message) int {
	length := 0
	for _, msg := range msgs {
		length += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			length += len(tc.Name) + len(tc.Arguments)
		}
	}
	return length
}

// utf8SafePrefix 从字符串开头截取不超过 limit 字节，并保证不切断 UTF-8 字符。
func utf8SafePrefix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}

	end := limit
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}

// utf8SafeSuffix 从字符串结尾反向截取不超过 limit 字节，并保证不切断 UTF-8 字符。
func utf8SafeSuffix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}

	start := len(content) - limit
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	return content[start:]
}
