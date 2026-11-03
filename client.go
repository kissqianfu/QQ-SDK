package qqbot

import (
	"context"
	"errors"
	"sync"

	"github.com/kissqianfu/QQ-SDK/api"
	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/qqtypes"
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

	mu        sync.Mutex
	cancel    context.CancelFunc
	running   bool
	closed    bool
	closeOnce sync.Once // done/events 只能关一次：Run 收尾与 Close 并发时防双重 close panic
}

// New 只校验配置并构造 REST 客户端，不建立任何连接（方便测试构造）。
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
	return &Client{cfg: cfg, api: apiClient, events: make(chan *event.Event, eventBuffer), done: make(chan struct{})}, nil
}

// Run 阻塞运行接入通道，把事件写入 Events()；ctx 取消后优雅退出并关闭事件通道。
//
// 同一 Client 只允许 Run 一次：重复调用返回错误（SDK 内部管理重连/心跳，外部不用管）。
func (c *Client) Run(ctx context.Context) error {
	if c.cfg.Transport == nil {
		// 没有接入通道时 Run 立即返回；同时关闭事件通道，保证 range Events() 的消费者
		// 不会被永久阻塞（与"Run 返回后 SDK 负责关闭 Events"的注释一致）。
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.closeOnce.Do(func() {
			close(c.done)
			close(c.events)
		})
		return errors.New("qqbot: Transport 未配置，无法 Run")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil // Close 已把事件通道关闭，直接退出（不重复 close）
	}
	if c.running {
		c.mu.Unlock()
		return errors.New("qqbot: Run 已在运行，不能重复调用")
	}
	c.running = true
	c.cancel = cancel
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.running = false
		// 必须置 closed：Run 收尾时已 close(events)，若之后再次 Run，
		// transport 往 events 写会 panic: send on closed channel。
		// 置位后第二次 Run 在入口直接返回 nil（与"Close 后 Run"同一语义）。
		c.closed = true
		c.mu.Unlock()
		c.closeOnce.Do(func() {
			close(c.done)
			close(c.events)
		})
	}()

	return c.cfg.Transport.Run(runCtx, func(ev *event.Event) error {
		select {
		case c.events <- ev:
			return nil
		case <-runCtx.Done():
			return runCtx.Err()
		}
	})
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
	if c.cancel != nil {
		c.cancel()
	}
	running := c.running
	if !running {
		c.closeOnce.Do(func() {
			close(c.done)
			close(c.events)
		})
	}
	done := c.done
	c.mu.Unlock()

	if running {
		<-done // 等 Run 的 defer 关闭事件通道，避免消费者读半截
	}
	return nil
}
