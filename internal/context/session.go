package context

import (
	"sync"
	"time"

	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
)

// Session 代表了一次持续的人机交互过程。它负责维护该会话的完整历史。
type Session struct {
	ID        string
	WorkDir   string // 该会话绑定的物理工作区
	CreatedAt time.Time
	UpdatedAt time.Time

	planMode bool
	taskDir  string

	// 存放此 Session 中所有的用户输入、大模型回复和工具调用结果
	history []schema.Message
	mu      sync.RWMutex // 读写锁，防止并发读写历史时发生 Data Race
}

func NewSession(id string, workDir string) *Session {
	return &Session{
		ID:        id,
		WorkDir:   workDir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		history:   make([]schema.Message, 0),
	}
}

// ConfigurePlanMode 设置当前 Session 的计划模式和任务状态目录。
func (s *Session) ConfigurePlanMode(enabled bool, taskDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.planMode = enabled
	if taskDir != "" {
		s.taskDir = taskDir
	}
	s.UpdatedAt = time.Now()
}

// PlanModeSnapshot 返回当前计划模式配置。
func (s *Session) PlanModeSnapshot() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.planMode, s.taskDir
}

// Append 线程安全地向 Session 中追加消息
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()

	// 【持久化预留点】：在真实的工业级实现中，我们会在这里将 s.history
	// 以 JSONL 格式追加到持久化存储中。
}

// GetWorkingMemory 从完整历史中截取最近的 N 条消息，形成短期工作记忆。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.history)
	if total <= limit || limit <= 0 {
		// 如果历史总量小于限制，或者不设限，全量返回。
		res := make([]schema.Message, total)
		copy(res, s.history)
		return res
	}

	// 先定位目标窗口起点；如果起点不是普通用户消息，就向前扩展到最近一条用户输入。
	// 否则 ReAct 工具循环达到窗口上限后，Provider 可能收到以 Assistant ToolCall 开头的非法消息序列。
	start := total - limit
	for start > 0 {
		if s.history[start].Role == schema.RoleUser && s.history[start].ToolCallID == "" {
			break
		}
		start--
	}

	// 截取从用户输入边界到最新的连续历史。
	res := make([]schema.Message, total-start)
	copy(res, s.history[start:])

	// 【驾驭防线】：大模型 API 强制要求历史消息的连续性。
	// 如果截断后的首条消息是失去 ToolCall 的 ToolResult，
	// 必须将其舍弃，避免 Provider 返回协议错误。
	for len(res) > 0 {
		if res[0].Role == schema.RoleUser && res[0].ToolCallID != "" {
			res = res[1:]
		} else {
			break
		}
	}

	return res
}

// SessionManager 按外部会话标识管理相互隔离的 Session。
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// GlobalSessionMgr 与教学快照保持一致，作为进程级 Session 注册表。
var GlobalSessionMgr = &SessionManager{
	sessions: make(map[string]*Session),
}

// GetOrCreate 获取或创建一个会话
func (sm *SessionManager) GetOrCreate(id string, workDir string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, exists := sm.sessions[id]; exists {
		return sess
	}
	sess := NewSession(id, workDir)
	sm.sessions[id] = sess
	return sess
}
