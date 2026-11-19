package qqbot

// ingress.go 是内置接入通道（ws / webhook）的装配层。
// 事件只从 Events() 出；收尾顺序固定：关 stop → 关 webhook handler → 关 done/events。

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kissqianfu/QQ-SDK/common/qqtypes"
	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/transport"
	"github.com/kissqianfu/QQ-SDK/core/transport/webhook"
	"github.com/kissqianfu/QQ-SDK/core/transport/ws"
)

// loggerOrDiscard 落实"Config.Logger == nil = 静默"的约定：直接传 nil 给内置通道
// 会回落到 slog.Default() 往 stderr 打日志，与文档相反。
func loggerOrDiscard(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

// buildIngress 按 Config.Mode 装配内置通道；Validate 已保证参数互斥/取值合法。
func (c *Client) buildIngress() error {
	switch c.cfg.Mode {
	case "":
		return nil // 兼容旧用法：由宿主提供 Transport（或纯 REST）
	case ModeWS:
		opts := ws.Options{}
		if c.cfg.WSOptions != nil {
			opts = *c.cfg.WSOptions // 值拷贝：不把门面回填写回调用方的结构体
		}
		opts.AppID = qqtypes.FirstNonEmpty(opts.AppID, c.cfg.AppID)
		// Token 硬覆盖：WS 鉴权必须复用 REST 的 token 缓存与 401 换票（Validate 已禁止用户自设）。
		opts.Token = c.api.Token
		// 代理/自定义 CA 必须与 REST 共用：否则会出现"能发消息、收不到事件"。
		if opts.HTTP == nil {
			opts.HTTP = c.cfg.HTTP
		}
		if opts.SessionStore == nil {
			// 不注入则每次断线都 Identify，断线窗口内的事件会丢（只有 Resume 会补）。
			opts.SessionStore = ws.NewMemorySession()
		}
		if opts.Logger == nil {
			opts.Logger = loggerOrDiscard(c.cfg.Logger)
		}
		tr, err := ws.New(opts)
		if err != nil {
			return fmt.Errorf("qqbot: 构造 ws 通道: %w", err)
		}
		c.cfg.Transport = tr
		return nil
	case ModeWebhook:
		opts := webhook.Options{}
		if c.cfg.WebhookOptions != nil {
			opts = *c.cfg.WebhookOptions
		}
		opts.Secret = qqtypes.FirstNonEmpty(opts.Secret, c.cfg.AppSecret)
		opts.AppID = qqtypes.FirstNonEmpty(opts.AppID, c.cfg.AppID)
		if opts.Logger == nil {
			opts.Logger = loggerOrDiscard(c.cfg.Logger)
		}
		// OnEvent 由门面独占：Validate 已禁止宿主自行设置。
		opts.OnEvent = func(ctx context.Context, ev *event.Event) {
			// stop 优先判定让 drain 语义确定：Close 后残留事件立即放弃
			// （webhook ACK 已发、平台不会重推），不再随机分摊给消费方。
			select {
			case <-c.stop:
				return
			default:
			}
			// ctx 是 OnEventTimeout 包出来的：回调超时/停机都不能阻塞 webhook worker。
			c.deliver(ev, ctx.Done())
		}
		h, err := webhook.New(opts)
		if err != nil {
			return fmt.Errorf("qqbot: 构造 webhook 通道: %w", err)
		}
		c.wh = h
		return nil
	default:
		return fmt.Errorf("qqbot: 未知 Mode %q", c.cfg.Mode)
	}
}

// deliver 把事件投递到 Events()。cancel 非 nil 时它关闭就放弃（ws 的 runCtx）；
// nil 在 select 中永久阻塞 = 不监听，于是 webhook/ws 两种模式共用同一段投递逻辑。
// 返回 false 表示没投进去，由调用方决定如何报告。
func (c *Client) deliver(ev *event.Event, cancel <-chan struct{}) bool {
	// 读锁 + eventsClosed：finalize 关 events 前先拿写锁，保证不会有 send on closed。
	// 这里的阻塞发送是刻意的（有界缓冲形成反压）：finalize 先关 stop，发送方随即返回。
	c.evMu.RLock()
	defer c.evMu.RUnlock()
	if c.eventsClosed {
		return false
	}
	select {
	case c.events <- ev:
		return true
	case <-cancel:
		return false
	case <-c.stop:
		return false
	}
}

// WebhookHTTPHandler 返回 webhook 回调处理器，业务挂到自己的路由（如 /alertbot/callback）。
// 非 webhook 模式返回 nil；应在启动 HTTP server 之前调用。
func (c *Client) WebhookHTTPHandler() *webhook.Handler { return c.wh }

// runWebhook 是 webhook 模式的 Run：只等取消，收尾统一在 finalize 里做（先关 stop 再关 handler，避免死锁）。
func (c *Client) runWebhook(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// finalize 是两种模式共用的收尾：只执行一次，顺序不可调换。
func (c *Client) finalize() {
	c.stopOnce.Do(func() { close(c.stop) })
	if c.wh != nil {
		c.wh.Close() // 停收（新请求 503，平台重推）→ 等已入队事件处理完（此时 stop 已关，worker 能退出）
	}
	c.closeOnce.Do(func() {
		// 先关 events 再关 done：Close 等到 done 返回时，events 一定已经关闭。
		c.evMu.Lock()
		c.eventsClosed = true
		close(c.events)
		c.evMu.Unlock()
		close(c.done)
	})
}

// wsEmit 是 ws 模式的事件出口：ctx 取消要返回错误让 transport 停止（既有语义）。
func (c *Client) wsEmit(ctx context.Context) func(*event.Event) error {
	return func(ev *event.Event) error {
		if c.deliver(ev, ctx.Done()) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return transport.ErrClosed
	}
}
