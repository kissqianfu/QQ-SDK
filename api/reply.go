package api

// reply.go 是"被动回复锚点降级链"的唯一实现（v0.3.3）。
//
// 为什么需要：平台有两个都叫 "id" 的字段——
//   - 外层信封 id：发送接口 event_id 用它；
//   - 事件体 d.id：消息事件是 msg_id，互动事件是裸 interaction_id（只能用于 /interactions）。
// 混用会得到 40034025。宿主编排"先试哪个、失败了换哪个"属于协议知识，收敛进 SDK。
//
// 语义边界：
//   - 只在 IsAnchorRejected（锚点被拒、未送达）时换下一个锚点；
//   - IsDuplicate（40054005）= 上一次已发送成功 → **当成功返回**，绝不重发（否则用户收到两条）；
//   - 其它错误（频控/内容违规/网络）直接返回，不做锚点降级。

import (
	"context"
	"errors"
	"fmt"

	"github.com/kissqianfu/QQ-SDK/apierr"
	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
)

// ReplyOptions 控制 SendReply 的行为。
type ReplyOptions struct {
	// Seq 是 msg_seq 取号器（按锚点分别计数）。nil 时每次用 1——
	// 同一锚点要多次回复时必须传，否则第二次会撞 40054005。
	Seq send.SeqAllocator
	// MaxAttempts 限制最多尝试几个候选锚点（0 = 全部）。
	MaxAttempts int
}

// SendReply 按事件的候选锚点发送一条被动回复，锚点被拒时自动降级到下一个。
//
// build 必须每次返回**全新**的 send.Message：SendReply 会往消息上挂锚点与 msg_seq，
// 复用同一个对象会串数据。示例：
//
//	res, err := c.Sender().SendReply(ctx, target, ev,
//	    func() *send.Message { return send.Markdown("收到") }, api.ReplyOptions{Seq: seqs})
//
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
		return nil, err // 频控/内容/权限等：换锚点也没用
	}
	return nil, fmt.Errorf("api: 所有被动回复锚点均被拒（共 %d 个）: %w", len(rejected), errors.Join(rejected...))
}

// applyAnchor 把锚点挂到消息上，并在需要时取 msg_seq。
//
// 复用 send 包的 ReplyToWithSeq / ReplyToEventWithSeq：seq 分配（按锚点计数）与
// "锚点+seq 配对"的校验只有 send 一处实现，这里不再重复。
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
		return nil, fmt.Errorf("api: 未知锚点类型 %d", a.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("api: 挂锚点 %s 失败: %w", a, err)
	}
	return m, nil
}
