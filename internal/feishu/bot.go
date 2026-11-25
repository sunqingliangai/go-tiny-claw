// internal/feishu/bot.go
package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	ctxpkg "github.com/sunqingliangai/go-tiny-claw/internal/context"
	"github.com/sunqingliangai/go-tiny-claw/internal/engine"
	"github.com/sunqingliangai/go-tiny-claw/internal/schema"
	"github.com/sunqingliangai/go-tiny-claw/internal/tools"
)

const (
	defaultQueueSize     = 8
	defaultTaskTimeout   = 10 * time.Minute
	maxPromptRuneCount   = 12000
	messageDedupTTL      = 10 * time.Minute
	maxDedupMessageCount = 4096
	planModeUsage        = "使用 /plan <任务> 开启计划模式，或使用 /plan off 关闭计划模式。"
)

// Config 封装飞书长连接运行参数。App Secret 只从环境变量注入。
type Config struct {
	AppID       string
	AppSecret   string
	WorkDir     string
	QueueSize   int
	TaskTimeout time.Duration
	ApproverIDs []string
}

// Bot 将飞书长连接事件桥接到 Agent Engine。
type Bot struct {
	appID       string
	appSecret   string
	client      *lark.Client
	engine      *engine.AgentEngine
	workDir     string
	queue       chan inboundMessage
	seen        *messageDeduper
	taskTimeout time.Duration
	approvals   *approvalManager
}

type inboundMessage struct {
	MessageID   string
	ChatID      string
	SenderID    string
	Prompt      string
	PlanControl planControl
}

type planControl int

const (
	planControlNone planControl = iota
	planControlEnable
	planControlDisable
)

func NewBot(eng *engine.AgentEngine, cfg Config) (*Bot, error) {
	if eng == nil {
		return nil, fmt.Errorf("agent engine 不能为空")
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, fmt.Errorf("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET")
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		return nil, fmt.Errorf("工作目录不能为空")
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = defaultTaskTimeout
	}

	bot := &Bot{
		appID:       cfg.AppID,
		appSecret:   cfg.AppSecret,
		client:      lark.NewClient(cfg.AppID, cfg.AppSecret),
		engine:      eng,
		workDir:     cfg.WorkDir,
		queue:       make(chan inboundMessage, cfg.QueueSize),
		seen:        newMessageDeduper(messageDedupTTL, maxDedupMessageCount),
		taskTimeout: cfg.TaskTimeout,
	}
	bot.approvals = newApprovalManager(cfg.ApproverIDs, bot.sendApprovalNotice)
	return bot, nil
}

// Start 建立飞书 WebSocket 长连接，并启动 Workspace 串行 Worker。
// 长连接模式不需要公网回调地址、Verification Token 或 Encrypt Key。
func (b *Bot) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		b.runWorker(runCtx)
	}()

	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(b.handleMessage)

	wsClient := larkws.NewClient(
		b.appID,
		b.appSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithOnReady(func() {
			log.Println("[Feishu] 长连接已建立，可以回到开放平台配置“使用长连接接收事件”")
		}),
		larkws.WithOnReconnecting(func() {
			log.Println("[Feishu] 长连接正在重连...")
		}),
		larkws.WithOnReconnected(func() {
			log.Println("[Feishu] 长连接已恢复")
		}),
		larkws.WithOnError(func(err error) {
			log.Printf("[Feishu] 长连接错误: %v", err)
		}),
	)

	err := wsClient.Start(runCtx)
	cancel()
	<-workerDone
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (b *Bot) runWorker(ctx context.Context) {
	for {
		select {
		case task := <-b.queue:
			b.handleAgentTask(ctx, task)
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bot) handleMessage(_ context.Context, event *larkim.P2MessageReceiveV1) error {
	log.Printf("ssss进入handleMessage")
	// 长连接事件 Handler 必须尽快返回。这里只做轻量校验和入队。
	// 飞书事件字段大量使用指针，先排除事件或关键对象为空的情况，避免解引用 panic。
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Message == nil {
		log.Printf("[Feishu] 收到无效事件，忽略")
		return nil
	}

	sender := event.Event.Sender
	message := event.Event.Message
	// 只处理真实用户发送的文本消息，忽略机器人消息和其他消息类型。
	if sender.SenderType == nil || *sender.SenderType != "user" {
		return nil
	}
	if message.MessageType == nil || *message.MessageType != "text" {
		return nil
	}
	if message.MessageId == nil || message.ChatId == nil || message.Content == nil {
		return nil
	}
	senderID, ok := resolveFeishuSenderID(sender)
	if !ok {
		log.Printf("[Feishu] 消息缺少发送者 ID，忽略")
		return nil
	}

	// 将事件中的指针值复制出来，后续校验和任务入队都使用这些稳定值。
	messageID := *message.MessageId
	chatID := *message.ChatId
	rawContent := *message.Content

	log.Printf("[Feishu] 收到消息事件 message_id=%s chat_id=%s", messageID, chatID)

	// 飞书文本消息的 Content 是 JSON 字符串，例如 {"text":"hello"}。
	var content textMessageContent
	if err := json.Unmarshal([]byte(rawContent), &content); err != nil {
		log.Printf("[Feishu] 解析文本消息失败 message_id=%s: %v", messageID, err)
		return nil
	}

	// 去掉机器人 mention 和首尾空白；空消息直接忽略。
	prompt := strings.TrimSpace(stripBotMentions(content.Text, message.Mentions))
	if prompt == "" {
		return nil
	}

	// 飞书可能重复推送同一事件；message_id TTL 去重同时覆盖审批命令和普通任务。
	if !b.seen.MarkIfNew(messageID) {
		log.Printf("[Feishu] 忽略重复推送 message_id=%s", messageID)
		return nil
	}

	// 审批命令直接唤醒正在等待的工具协程，不进入 Agent 任务队列。
	if approvalID, allowed, ok := parseApprovalCommand(prompt); ok {
		result := b.approvals.ResolveApproval(chatID, senderID, approvalID, allowed)
		b.notifyAsync(messageID, result)
		return nil
	}

	// 先解析 /plan 控制命令，再对真正的任务文本做长度校验。
	taskPrompt, planControl := parsePlanControl(prompt)
	if planControl == planControlEnable && taskPrompt == "" {
		b.notifyAsync(messageID, planModeUsage)
		return nil
	}
	if utf8.RuneCountInString(taskPrompt) > maxPromptRuneCount {
		b.notifyAsync(messageID, fmt.Sprintf("消息过长，请控制在 %d 个字符以内。", maxPromptRuneCount))
		return nil
	}

	// inboundMessage 只携带 Worker 执行所需的不可变数据。
	task := inboundMessage{
		MessageID:   messageID,
		ChatID:      chatID,
		SenderID:    senderID,
		Prompt:      taskPrompt,
		PlanControl: planControl,
	}

	// 非阻塞入队：Handler 不在飞书事件线程中运行 Agent，队列满时快速拒绝并异步提示。
	select {
	case b.queue <- task:
		log.Printf("[Feishu] 已接收消息 message_id=%s chat_id=%s", messageID, chatID)
	default:
		log.Printf("[Feishu] 任务队列已满，拒绝 message_id=%s", messageID)
		b.notifyAsync(messageID, "当前任务繁忙，请稍后重试。")
	}

	return nil
}

func (b *Bot) handleAgentTask(parent context.Context, task inboundMessage) {
	// 当前所有任务共享 WorkDir，因此使用单 Worker 串行执行，避免物理文件竞争。
	// 每个任务使用独立超时 Context；父 Context 取消时，正在运行的 Agent 也能收到取消信号。
	log.Printf("ssss进入handleAgentTask message_id=%s chat_id=%s plan_control=%d", task.MessageID, task.ChatID, task.PlanControl)
	ctx, cancel := context.WithTimeout(parent, b.taskTimeout)
	defer cancel()

	// 当前 Run 的审批身份通过 Context 传给 Registry，避免使用跨聊天全局单例。
	ctx = withApprovalRunInfo(ctx, approvalRunInfo{
		ChatID:          task.ChatID,
		RequesterID:     task.SenderID,
		SourceMessageID: task.MessageID,
	})
	ctx = tools.WithApprover(ctx, b.approvals)

	// Reporter 绑定原始飞书消息，最终结果和错误会回复到对应的消息会话。
	reporter := newFeishuReporter(b.client, task.MessageID)

	// 按飞书 chat_id 隔离会话：同一个群或单聊共享连续上下文。
	session := ctxpkg.GlobalSessionMgr.GetOrCreate(task.ChatID, b.workDir)

	// 每个聊天使用稳定哈希目录，避免不同群同时把计划写到根目录而互相覆盖。
	taskDir := planTaskDir(task.ChatID)

	// /plan 和 /plan off 是控制命令：只更新 Session 状态，不把命令文本发给模型。
	switch task.PlanControl {
	case planControlEnable:
		session.ConfigurePlanMode(true, taskDir)
		log.Printf("[Feishu] Plan Mode 已开启 chat_id=%s task_dir=%s", task.ChatID, taskDir)
	case planControlDisable:
		session.ConfigurePlanMode(false, taskDir)
		log.Printf("[Feishu] Plan Mode 已关闭 chat_id=%s", task.ChatID)
		reporter.OnMessage(ctx, "计划模式已关闭啦。")
		return
	}

	// 用户任务先进入 Session；Engine 会把 Session 中最近的有效消息投影为 Working Memory。
	session.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: task.Prompt,
	})

	// Run 会阻塞到本轮 ReAct 结束；单 Worker 因此天然保证同一时刻只有一个 Agent Run。
	if err := b.engine.Run(ctx, session, reporter); err != nil {
		var loopErr *engine.LoopLimitError
		if errors.As(err, &loopErr) {
			// Engine 已把明确的停止原因写入 Session，并通过 Reporter 回复用户。
			// 这里只记录受控终止，不再追加一层通用的“运行失败”消息。
			log.Printf(
				"[Feishu] 检测到连续失败循环，Agent 已受控停止 message_id=%s tool=%s count=%d",
				task.MessageID,
				loopErr.ToolName,
				loopErr.Count,
			)
			return
		}

		log.Printf("[Feishu] Agent 运行失败 message_id=%s: %v", task.MessageID, err)
		// 父 Context 已取消时不再尝试发送错误消息，避免进程退出阶段产生无意义网络请求。
		if parent.Err() == nil {
			reporter.SendError(fmt.Sprintf("Agent 运行失败: %v", err))
		}
	}
}

func (b *Bot) sendApprovalNotice(ctx context.Context, notice approvalNotice) error {
	arguments := truncateRunes(notice.Arguments, 2000)
	text := fmt.Sprintf(`高危操作审批请求

工具: %s
原因: %s
参数:
%s

审批 ID: %s
有效期至: %s

请回复 "approve %s" 或 "reject %s"。
只接受发起本次任务的用户或配置的审批管理员操作。`,
		notice.ToolName,
		notice.Reason,
		arguments,
		notice.ApprovalID,
		notice.ExpiresAt.Format("2006-01-02 15:04:05"),
		notice.ApprovalID,
		notice.ApprovalID,
	)
	uuid := makeReplyUUID(notice.SourceMessageID, "approval:"+notice.ApprovalID, 0)
	return replyTextWithUUID(ctx, b.client, notice.SourceMessageID, uuid, text)
}

func resolveFeishuSenderID(sender *larkim.EventSender) (string, bool) {
	if sender == nil || sender.SenderId == nil {
		return "", false
	}
	for _, id := range []*string{
		sender.SenderId.UserId,
		sender.SenderId.OpenId,
		sender.SenderId.UnionId,
	} {
		if id != nil && strings.TrimSpace(*id) != "" {
			return strings.TrimSpace(*id), true
		}
	}
	return "", false
}

func parseApprovalCommand(prompt string) (approvalID string, allowed bool, ok bool) {
	fields := strings.Fields(prompt)
	if len(fields) != 2 {
		return "", false, false
	}
	switch strings.ToLower(fields[0]) {
	case "approve":
		return fields[1], true, true
	case "reject":
		return fields[1], false, true
	default:
		return "", false, false
	}
}

func parsePlanControl(prompt string) (string, planControl) {
	if prompt == "/plan" {
		return "", planControlEnable
	}
	if prompt == "/plan off" {
		return "", planControlDisable
	}
	if strings.HasPrefix(prompt, "/plan ") {
		task := strings.TrimSpace(strings.TrimPrefix(prompt, "/plan "))
		if task == "off" {
			return "", planControlDisable
		}
		return task, planControlEnable
	}
	return prompt, planControlNone
}

func planTaskDir(chatID string) string {
	// 使用稳定哈希隔离各聊天，避免外部 chat_id 直接进入文件路径。
	sum := sha256.Sum256([]byte("feishu:" + chatID))
	return fmt.Sprintf(".claw/tasks/%x", sum[:8])
}

func (b *Bot) notifyAsync(messageID, text string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := replyText(ctx, b.client, messageID, "notice", text); err != nil {
			log.Printf("[Feishu] 发送提示失败 message_id=%s: %v", messageID, err)
		}
	}()
}

type textMessageContent struct {
	Text string `json:"text"`
}

func stripBotMentions(text string, mentions []*larkim.MentionEvent) string {
	for _, mention := range mentions {
		if mention == nil || mention.Key == nil || mention.MentionedType == nil {
			continue
		}
		if *mention.MentionedType == "bot" {
			text = strings.ReplaceAll(text, *mention.Key, "")
		}
	}
	return text
}

type messageDeduper struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	ttl       time.Duration
	max       int
	lastSweep time.Time
}

func newMessageDeduper(ttl time.Duration, max int) *messageDeduper {
	return &messageDeduper{
		seen:      make(map[string]time.Time),
		ttl:       ttl,
		max:       max,
		lastSweep: time.Now(),
	}
}

func (d *messageDeduper) MarkIfNew(messageID string) bool {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sweepLocked(now)
	if _, exists := d.seen[messageID]; exists {
		return false
	}

	if len(d.seen) >= d.max {
		var oldestID string
		var oldestAt time.Time
		for id, seenAt := range d.seen {
			if oldestID == "" || seenAt.Before(oldestAt) {
				oldestID = id
				oldestAt = seenAt
			}
		}
		delete(d.seen, oldestID)
	}

	d.seen[messageID] = now
	return true
}

func (d *messageDeduper) sweepLocked(now time.Time) {
	if now.Sub(d.lastSweep) < time.Minute {
		return
	}
	for id, seenAt := range d.seen {
		if now.Sub(seenAt) > d.ttl {
			delete(d.seen, id)
		}
	}
	d.lastSweep = now
}
