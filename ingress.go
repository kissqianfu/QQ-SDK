package qqbot

// ingress.go 是"内置接入通道"的装配层（Config.Mode = ws / webhook）。
//
// 设计取舍（对照 PROPOSAL-channel.md 的方案 B）：
//   - ws 模式：仍走 transport.Transport 抽象，Run 委托给它，行为与手工装配一致；
//   - webhook 模式：**不**把 http.Handler 硬包成 Transport。webhook 天然是"请求驱动"，
//     包装成 Run 循环会多一层缓冲，还引出"取消时先等 Handler 还是先排水"的死锁。
//     这里直接让 Handler 的 OnEvent 投递到 Events()，Run 只负责等取消后收尾。
//
// 统一不变量（两种模式一致）：
//   - 事件只从 Events() 出，宿主只需 range 一次；
//   - Run 阻塞到 ctx 取消/Close 后返回；
//   - 收尾顺序：关闭 stop（让在途投递立即返回）→ 关闭 webhook handler（等 worker 退出）
//     → 关闭 done/events。顺序反了就是死锁或 send-on-closed。

import (
	"context"
	"fmt"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/qqtypes"
	"github.com/kissqianfu/QQ-SDK/transport"
	"github.com/kissqianfu/QQ-SDK/transport/webhook"
	"github.com/kissqianfu/QQ-SDK/transport/ws"
)

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
		opts.Token = c.api.Token // 复用 REST 的 token 缓存与 401 换票
		if opts.SessionStore == nil {
			// 不注入则每次断线都 Identify，断线窗口内的事件会丢（只有 Resume 会补）。
			opts.SessionStore = ws.NewMemorySession()
		}
		if opts.Logger == nil {
			opts.Logger = c.cfg.Logger
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
			opts.Logger = c.cfg.Logger
		}
		// OnEvent 由门面独占：Validate 已禁止宿主自行设置。
		opts.OnEvent = func(_ context.Context, ev *event.Event) { c.pushEvent(ev) }
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
	select {
	case c.events <- ev:
		return true
	case <-cancel:
		return false
	case <-c.stop:
		return false
	}
}

// pushEvent 把 webhook 入站事件投递到 Events()。
//
// 满时阻塞 → 反压到 handler 队列（其满了会走 OnDrop，不会无界占内存）；
// 收尾时 stop 关闭，在途投递立即返回，避免与 close(events) 竞态。
func (c *Client) pushEvent(ev *event.Event) { c.deliver(ev, nil) }

// WebhookHTTPHandler 返回 webhook 回调处理器，业务挂到自己的路由（如 /alertbot/callback）。
// 非 webhook 模式返回 nil；应在启动 HTTP server 之前调用。
func (c *Client) WebhookHTTPHandler() *webhook.Handler { return c.wh }

// runWebhook 是 webhook 模式的 Run：只等取消，收尾统一在 endRun/finalize 里做。
//
// 为什么不在这里直接调 h.Close()：endRun 的 finalize 会先关 stop 再关 handler——
// 反过来的话，worker 可能正阻塞在"往满的 Events 投递"上，而 Close 等 worker，
// 就变成 Close 等 worker、worker 等消费、消费等 Close 的三方死锁。
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
		close(c.done)
		close(c.events)
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
