package api

// reply.go 是被动回复锚点降级链的实现：只在 IsAnchorRejected 时换下一个锚点；
// IsDuplicate 视为成功返回，绝不重发；其它错误直接返回。

import (
	"context"
	"errors"
	"fmt"

	"github.com/kissqianfu/QQ-SDK/core/apierr"
	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/send"
)

// ReplyOptions 控制 SendReply 的行为。
type ReplyOptions struct {
	// Seq 是 msg_seq 取号器；nil 时每次用 1（同一锚点多次回复必须传）。
	Seq send.SeqAllocator
	// MaxAttempts 限制最多尝试几个候选锚点（0 = 全部）。
	MaxAttempts int
}

// SendReply 按事件的候选锚点发送一条被动回复，锚点被拒时自动降级到下一个。
// build 必须每次返回全新的 send.Message（SendReply 会往消息上挂锚点与 msg_seq）。
// 成功与"已被平台去重"都返回 (非 nil, nil)；返回值为 nil 表示没送达。
func (c *Client) SendReply(ctx context.Context, target Target, ev *event.Event, build func() *send.Message, opts ReplyOptions) (*Result, error) {
	if ev == nil {
		return nil, fmt.Errorf("api: SendReply 需要事件（锚点来源）")
	}
	if build == nil {
		return nil, fmt.Errorf("api: SendReply 需要构建函数 build()")
	}
	anchors := ev.ReplyAnchors()
	if len(anchors) == 0 {
		return nil, fmt.Errorf("api: 事件 %s 没有可用的被动回复锚点", ev.Type)
	}
	if opts.MaxAttempts > 0 && len(anchors) > opts.MaxAttempts {
		anchors = anchors[:opts.MaxAttempts]
	}

	var rejected []error
	for _, a := range anchors {
		msg := build()
		if msg == nil {
			return nil, fmt.Errorf("api: SendReply 的 build() 返回 nil")
		}
		msg, err := applyAnchor(ctx, msg, a, opts.Seq)
		if err != nil {
			return nil, err
		}

		res, err := c.Send(ctx, target, msg)
		if err == nil {
			return res, nil
		}
		var ae *apierr.APIError
		if !errors.As(err, &ae) {
			return nil, err // 网络/编码等：不换锚点
		}
		if ae.IsDuplicate() {
			// 同一锚点 + msg_seq 已发送成功：视为成功，绝不能换锚点重发。
			return &Result{}, nil
		}
		if ae.IsAnchorRejected() {
			rejected = append(rejected, fmt.Errorf("锚点 %s 被拒: %w", a, err))
			continue
		}
		return nil, err // 频控/内容/权限等：不重试
	}
	return nil, fmt.Errorf("api: 所有被动回复锚点均被拒（共 %d 个）: %w", len(rejected), errors.Join(rejected...))
}

// applyAnchor 把锚点挂到消息上，并在需要时取 msg_seq（复用 send 包的 WithSeq 方法）。
func applyAnchor(ctx context.Context, msg *send.Message, a event.Anchor, seqs send.SeqAllocator) (*send.Message, error) {
	if seqs == nil {
		if a.Kind == event.AnchorMsgID {
			return msg.ReplyTo(a.ID, 1), nil
		}
		return msg.ReplyToEvent(a.ID, 1), nil
	}
	var (
		m   *send.Message
		err error
	)
	switch a.Kind {
	case event.AnchorMsgID:
		m, err = msg.ReplyToWithSeq(ctx, seqs, a.ID)
	case event.AnchorEventID:
		m, err = msg.ReplyToEventWithSeq(ctx, seqs, a.ID)
	default:
		// 防御分支：ReplyAnchors 只产 MsgID/EventID 两种锚点。
		return nil, fmt.Errorf("api: 未知锚点类型 %d", a.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("api: 挂锚点 %s 失败: %w", a, err)
	}
	return m, nil
}
