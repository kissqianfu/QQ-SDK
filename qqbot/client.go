package qqbot

import (
	"context"
	"errors"
	"sync"

	"github.com/kissqianfu/QQ-SDK/core/api"
	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/transport/webhook"
)

const (
	// eventBuffer 是事件通道容量：有界，消费慢会反压 transport。
	eventBuffer = 128
)

// Client 是 SDK 门面：一个 Client 对应一个机器人实例。
type Client struct {
	cfg    Config
	api    *api.Client
	events chan *event.Event
	done   chan struct{} // Run 退出（或从未 Run 就 Close）时关闭
	// wh 是 webhook 模式的回调处理器；其余模式为 nil（见 ingress.go）。
	wh *webhook.Handler
	// stop 在收尾时先于 events 关闭：让在途投递立即返回，避免 send-on-closed。
	stop chan struct{}

	mu        sync.Mutex
	cancel    context.CancelFunc
	running   bool
	closed    bool
	closeOnce sync.Once // done/events 只能关一次
	stopOnce  sync.Once // stop 只能关一次

	// evMu/eventsClosed 保护 events 的关闭与发送：自定义 Transport 可能在 Run 返回后
	// 仍从其他 goroutine 调 emit，只靠 stop 通道无法阻止 send on closed channel。
	evMu         sync.RWMutex
	eventsClosed bool
}

// New 只校验配置并构造 REST 客户端（按需装配内置通道），不建立连接（方便测试构造）。
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	apiClient, err := api.New(api.Config{
		AppID:     cfg.AppID,
		AppSecret: cfg.AppSecret,
		HTTP:      cfg.HTTP,
		Tokens:    cfg.Tokens,
		Logger:    cfg.Logger,
		BaseURL:   cfg.BaseURL,
		TokenURL:  cfg.TokenURL,
		Quota:     cfg.Quota,

		RetryNonIdempotent: cfg.RetryNonIdempotent,
		Retry:              cfg.Retry,
		MaxContentLen:      cfg.MaxContentLen,
	})
	if err != nil {
		return nil, err
	}
	c := &Client{
		cfg:    cfg,
		api:    apiClient,
		events: make(chan *event.Event, eventBuffer),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
	}
	if err := c.buildIngress(); err != nil {
		return nil, err
	}
	return c, nil
}

// Run 阻塞运行接入通道，把事件写入 Events()；ctx 取消后优雅退出并关闭事件通道。
// 同一 Client 只允许 Run 一次；ws 模式委托 transport.Run，webhook 模式只等取消。
func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx, cancel, ok, err := c.beginRun(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil // Close 已发生：事件通道已关闭，直接退出
	}
	defer cancel()
	defer c.endRun()

	if c.cfg.Transport == nil && c.wh == nil {
		// 没有接入通道：endRun 的 defer 会 finalize，事件通道随之关闭。
		return errors.New("qqbot: 接入通道未配置（Config.Mode 或 Config.Transport），无法 Run")
	}

	if c.wh != nil {
		return c.runWebhook(runCtx)
	}
	return c.cfg.Transport.Run(runCtx, c.wsEmit(runCtx))
}

// beginRun 做"只允许一次 Run"的登记；ok=false 表示 Client 已关闭（Run 应静默返回）。
func (c *Client) beginRun(parent context.Context) (runCtx context.Context, cancel context.CancelFunc, ok bool, err error) {
	runCtx, cancel = context.WithCancel(parent)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		cancel()
		return nil, nil, false, nil
	}
	if c.running {
		cancel()
		return nil, nil, false, errors.New("qqbot: Run 已在运行，不能重复调用")
	}
	c.running = true
	c.cancel = cancel
	return runCtx, cancel, true, nil
}

// endRun 是 Run 的收尾：置位 closed 并执行统一 finalize。
func (c *Client) endRun() {
	c.mu.Lock()
	c.running = false
	// 必须置 closed：Run 收尾时已 close(events)，再 Run 会 send on closed channel。
	c.closed = true
	c.mu.Unlock()
	c.finalize()
}

// Events 返回只读事件通道；Run 返回后（或 Close 后从未 Run）SDK 负责关闭，业务只 range。
func (c *Client) Events() <-chan *event.Event { return c.events }

// Sender 返回发送接口（REST 客户端：发消息 / Me / 素材等全部 REST 能力）。
func (c *Client) Sender() *api.Client { return c.api }

// Close 幂等关闭：取消 Run 的 ctx，并等待 Run 退出（事件通道随之关闭）。
// 从未 Run 过就 Close 时，事件通道立即关闭，消费者 range 直接结束。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		// 已进入关闭流程：等第一个关闭方真正收尾完成，否则调用方会误以为已关闭。
		<-c.done
		return nil
	}
	c.closed = true
	cancel := c.cancel
	running := c.running
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if !running {
		c.finalize()
	}
	<-c.done // 等 finalize 完成（含 webhook handler 排空、events 关闭）
	_ = c.api.Close()
	return nil
}
