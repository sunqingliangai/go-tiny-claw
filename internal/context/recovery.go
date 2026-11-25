// internal/context/recovery.go
package context

import (
	"fmt"
	"strings"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

// RecoveryManager 负责在工具执行失败时，根据结构化错误码和报错特征注入恢复建议。
type RecoveryManager struct{}

func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// AnalyzeAndInject 接收原始 ToolResult，返回保留错误事实并附带行动建议的新输出。
func (rm *RecoveryManager) AnalyzeAndInject(toolName string, result schema.ToolResult) string {
	hint := recoveryHintByCode(result.ErrorCode)
	if hint == "" {
		// 兼容尚未升级为结构化错误的工具或第三方工具。
		hint = recoveryHintByText(toolName, result.Output)
	}
	if hint == "" {
		return result.Output
	}

	return fmt.Sprintf("%s\n\n[系统救援指南]: %s", result.Output, hint)
}

func recoveryHintByCode(code schema.ErrorCode) string {
	switch code {
	case schema.ErrorCodeFileNotFound:
		return "路径可能不正确。请先使用 bash 的 ls 或 find 查找真实目录和文件名，不要继续猜测路径。"
	case schema.ErrorCodePermissionDenied:
		return "当前工具没有该路径的权限。不要反复重试相同操作，请检查工作区范围或选择可访问的替代文件。"
	case schema.ErrorCodeEditOldTextNotFound:
		return "old_text 与文件当前内容或缩进不一致。请先使用 read_file 重新读取文件，再使用准确内容发起编辑。"
	case schema.ErrorCodeEditOldTextAmbiguous:
		return "old_text 不够唯一。请加入更多上下相邻代码，确保只匹配目标位置。"
	case schema.ErrorCodeInvalidArguments:
		return "工具参数格式不正确。请检查必填字段、JSON 结构和字段类型后重新调用。"
	case schema.ErrorCodeBashCommandNotFound:
		return "系统没有该命令。请检查拼写，或选择已安装的替代命令。"
	case schema.ErrorCodeBashTimeout:
		return "命令执行超时。请缩小执行范围，检查命令是否需要交互输入，避免启动无法管理生命周期的常驻进程。"
	case schema.ErrorCodeBashExitNonzero:
		return "命令返回非零退出码。请先阅读 stderr，定位具体失败原因；不要在输入未变化时反复执行相同命令。"
	case schema.ErrorCodeBashStartFailed:
		return "Shell 或命令进程无法启动。请检查环境、命令路径和当前执行上下文。"
	case schema.ErrorCodeToolNotFound:
		return "请求的工具不存在。请只使用当前提供的工具列表。"
	case schema.ErrorCodeToolCanceled:
		return "工具执行已取消。不要自动重试，等待新的用户指令或确认任务是否仍然有效。"
	case schema.ErrorCodePolicyDenied:
		return "该操作被 Harness 安全策略拒绝。不要尝试换写法绕过，请选择策略允许的替代方案或询问用户。"
	case schema.ErrorCodeApprovalRejected:
		return "人工审批已拒绝该操作。不要原样重试，请询问用户新的指令或选择其他方案。"
	case schema.ErrorCodeApprovalTimeout:
		return "人工审批已超时，操作没有执行。不要自动重试，请让用户重新发起任务。"
	case schema.ErrorCodeApprovalCanceled:
		return "审批因任务取消或中断而失效，操作没有执行。不要自动重试。"
	case schema.ErrorCodeApprovalMismatch:
		return "审批绑定的动作与当前请求不一致，操作没有执行。需重新发起审批。"
	case schema.ErrorCodeApprovalUnavailable:
		return "当前没有可用的人工审批通道，操作没有执行。不要绕过审批继续尝试。"
	case schema.ErrorCodeToolInternal:
		return "工具发生内部错误。不要原样重复相同调用，请检查输入或改用其他可用工具。"
	}
	return ""
}

func recoveryHintByText(toolName string, rawError string) string {
	lowerError := strings.ToLower(rawError)

	switch toolName {
	case "edit_file":
		if strings.Contains(rawError, "在文件中未找到 old_text") || strings.Contains(rawError, "找不到该代码片段") {
			return "请先使用 read_file 重新读取文件，获取最新、准确的内容后再编辑。"
		}
		if strings.Contains(rawError, "匹配到了多处") || strings.Contains(rawError, "提供更多上下文") {
			return "请增加 old_text 的相邻代码上下文，确保替换目标唯一。"
		}

	case "read_file", "write_file":
		if strings.Contains(lowerError, "no such file or directory") {
			return "请先使用 bash 的 ls 或 find 查找正确路径。"
		}
		if strings.Contains(lowerError, "permission denied") {
			return "不要重试相同操作，请检查工作区权限边界。"
		}

	case "bash":
		if strings.Contains(lowerError, "command not found") {
			return "命令不存在，请改用已安装的替代命令。"
		}
		if strings.Contains(lowerError, "deadlineexceeded") || strings.Contains(rawError, "超时") {
			return "缩小命令范围，避免执行需要交互或长期运行的进程。"
		}
		if strings.Contains(lowerError, "syntax error") {
			return "检查 Shell 引号、转义和特殊字符后重新执行。"
		}
	}

	return ""
}
