package qqbot

import (
	"context"
	"errors"
	"sync"

	"github.com/kissqianfu/QQ-SDK/api"
	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/qqtypes"
	"github.com/kissqianfu/QQ-SDK/transport/webhook"
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
	// stop 在收尾时先于 events 关闭：让在途投递（pushEvent/wsEmit）立即返回，
	// 避免 send-on-closed 或"关 events 时还有发送者"。
	stop chan struct{}

	mu        sync.Mutex
	cancel    context.CancelFunc
	running   bool
	closed    bool
	closeOnce sync.Once // done/events 只能关一次：Run 收尾与 Close 并发时防双重 close panic
	stopOnce  sync.Once // stop 只能关一次
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
//
// 同一 Client 只允许 Run 一次：重复调用返回错误（SDK 内部管理重连/心跳，外部不用管）。
//
// ws 模式：委托给 transport.Run（心跳/重连/Resume 在内）；
// webhook 模式：等 ctx 取消（或 Close）后停收并收尾，事件由 WebhookHTTPHandler 推入 Events()。
func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.cfg.Transport == nil && c.wh == nil {
		// 没有接入通道时 Run 立即失败；同时关闭事件通道，保证 range Events() 的消费者
		// 不会被永久阻塞（与"Run 返回后 SDK 负责关闭 Events"的注释一致）。
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.finalize()
		return errors.New("qqbot: 接入通道未配置（Config.Mode 或 Config.Transport），无法 Run")
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
	// 必须置 closed：Run 收尾时已 close(events)，若之后再次 Run，
	// transport 往 events 写会 panic: send on closed channel。
	c.closed = true
	c.mu.Unlock()
	c.finalize()
}

// Events 返回只读事件通道；Run 返回后（或 Close 后从未 Run）SDK 负责关闭，业务只 range。
func (c *Client) Events() <-chan *event.Event { return c.events }

// Sender 返回发送接口（REST 客户端：发消息 / Me）。
func (c *Client) Sender() *api.Client { return c.api }

// Me 拉取机器人自身信息（启动时调用一次，失败应拒绝启动）。
func (c *Client) Me(ctx context.Context) (*qqtypes.User, error) { return c.api.Me(ctx) }

// Close 幂等关闭：取消 Run 的 ctx，并等待 Run 退出（事件通道随之关闭）。
// 从未 Run 过就 Close 时，事件通道立即关闭，消费者 range 直接结束。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.cancel
	running := c.running
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if running {
		<-c.done // 等 Run 的 defer 执行完 finalize（含 webhook handler 收尾）
		return nil
	}
	c.finalize()
	return nil
}
