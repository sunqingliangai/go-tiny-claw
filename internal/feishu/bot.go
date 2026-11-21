// internal/feishu/bot.go
package feishu

import (
	"context"
	"encoding/json"
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
	"github.com/sunqingliangai/go-tiny-claw/internal/engine"
)

const (
	defaultQueueSize     = 8
	defaultTaskTimeout   = 10 * time.Minute
	maxPromptRuneCount   = 12000
	messageDedupTTL      = 10 * time.Minute
	maxDedupMessageCount = 4096
)

// Config 封装飞书长连接运行参数。App Secret 只从环境变量注入。
type Config struct {
	AppID       string
	AppSecret   string
	QueueSize   int
	TaskTimeout time.Duration
}

// Bot 将飞书长连接事件桥接到 Agent Engine。
type Bot struct {
	appID       string
	appSecret   string
	client      *lark.Client
	engine      *engine.AgentEngine
	queue       chan inboundMessage
	seen        *messageDeduper
	taskTimeout time.Duration
}

type inboundMessage struct {
	MessageID string
	ChatID    string
	Prompt    string
}

func NewBot(eng *engine.AgentEngine, cfg Config) (*Bot, error) {
	if eng == nil {
		return nil, fmt.Errorf("agent engine 不能为空")
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, fmt.Errorf("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET")
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = defaultTaskTimeout
	}

	return &Bot{
		appID:       cfg.AppID,
		appSecret:   cfg.AppSecret,
		client:      lark.NewClient(cfg.AppID, cfg.AppSecret),
		engine:      eng,
		queue:       make(chan inboundMessage, cfg.QueueSize),
		seen:        newMessageDeduper(messageDedupTTL, maxDedupMessageCount),
		taskTimeout: cfg.TaskTimeout,
	}, nil
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
	// 长连接事件 Handler 必须尽快返回。这里只做轻量校验和入队。
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Message == nil {
		return nil
	}

	sender := event.Event.Sender
	message := event.Event.Message
	if sender.SenderType == nil || *sender.SenderType != "user" {
		return nil
	}
	if message.MessageType == nil || *message.MessageType != "text" {
		return nil
	}
	if message.MessageId == nil || message.ChatId == nil || message.Content == nil {
		return nil
	}

	messageID := *message.MessageId
	chatID := *message.ChatId
	rawContent := *message.Content

	var content textMessageContent
	if err := json.Unmarshal([]byte(rawContent), &content); err != nil {
		log.Printf("[Feishu] 解析文本消息失败 message_id=%s: %v", messageID, err)
		return nil
	}

	prompt := strings.TrimSpace(stripBotMentions(content.Text, message.Mentions))
	if prompt == "" {
		return nil
	}
	if utf8.RuneCountInString(prompt) > maxPromptRuneCount {
		b.notifyAsync(messageID, fmt.Sprintf("消息过长，请控制在 %d 个字符以内。", maxPromptRuneCount))
		return nil
	}
	if !b.seen.MarkIfNew(messageID) {
		log.Printf("[Feishu] 忽略重复推送 message_id=%s", messageID)
		return nil
	}

	task := inboundMessage{
		MessageID: messageID,
		ChatID:    chatID,
		Prompt:    prompt,
	}

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
	ctx, cancel := context.WithTimeout(parent, b.taskTimeout)
	defer cancel()

	reporter := newFeishuReporter(b.client, task.MessageID)
	if err := b.engine.Run(ctx, task.Prompt, reporter); err != nil {
		log.Printf("[Feishu] Agent 运行失败 message_id=%s: %v", task.MessageID, err)
		if parent.Err() == nil {
			reporter.SendError(fmt.Sprintf("Agent 运行失败: %v", err))
		}
	}
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
