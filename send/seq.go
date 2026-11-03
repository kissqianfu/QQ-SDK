package send

// seq.go 提供被动回复 msg_seq 的取号能力。
//
// 为什么需要：平台按「锚点(msg_id/event_id) + msg_seq」判重，重复会返回 40054005。
// 这个错误会被 apierr.IsDuplicate() 判定为"已发过、按成功处理"，于是**用户什么都收不到**，
// 而调用方以为成功——静默丢消息。所以 seq 必须单调递增，且键不能用错：
//   - 回复某条消息（ReplyTo）→ 键 = msg_id；
//   - 回复某个事件（ReplyToEvent）→ 键 = event_id。
// 两类锚点不能共用同一个计数器（同一条消息的 msg_id 与 event_id 无关）。
//
// SDK 只提供接口与内存实现；生产环境的"跨重启单调"必须由宿主注入持久化实现
// （DB 行锁 / Redis INCR），内存实现仅覆盖单进程内重连与测试。

import (
	"context"
	"fmt"
	"sync"
)

// SeqAllocator 为被动回复分配 msg_seq：返回当前值并自增（首次为 1）。
//
// anchor 是 msg_id 或 event_id；实现按 anchor 分别计数。
// 并发安全由实现保证（平台可能并发触发同一锚点的多次回复，例如按钮连点）。
type SeqAllocator interface {
	Next(ctx context.Context, anchor string) (int, error)
}

// maxMemorySeqKeys 是内存实现的锚点上限：锚点是消息/事件 id，天然只增不减，
// 不设上限会无界增长。超出按 FIFO 淘汰（被淘汰的锚点重新从 1 开始——那只可能在
// 消息早已过期后发生，不影响仍在有效期内的回复）。
const maxMemorySeqKeys = 4096

// memorySeq 是单进程内存实现（重启归零）。
type memorySeq struct {
	mu    sync.Mutex
	seqs  map[string]int
	order []string
}

// NewMemorySeq 创建内存 SeqAllocator：进程内单调，**重启后从 1 重新开始**。
// 生产环境如果要求"跨重启单调"，请实现自己的 SeqAllocator（DB/Redis）并注入。
func NewMemorySeq() SeqAllocator { return &memorySeq{seqs: make(map[string]int)} }

// Next 实现 SeqAllocator。
func (s *memorySeq) Next(ctx context.Context, anchor string) (int, error) {
	if anchor == "" {
		return 0, fmt.Errorf("send: seq 取号需要非空锚点（msg_id 或 event_id）")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seqs[anchor]; !ok {
		if len(s.seqs) >= maxMemorySeqKeys && len(s.order) > 0 {
			delete(s.seqs, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, anchor)
	}
	s.seqs[anchor]++
	return s.seqs[anchor], nil
}

// ReplyToWithSeq 用 SeqAllocator 取号并设置 msg_id 锚点（被动回复某条消息）。
func (m *Message) ReplyToWithSeq(ctx context.Context, a SeqAllocator, msgID string) (*Message, error) {
	if a == nil {
		return m, fmt.Errorf("send: SeqAllocator 不能为 nil")
	}
	seq, err := a.Next(ctx, msgID)
	if err != nil {
		return m, err
	}
	return m.ReplyTo(msgID, seq), nil
}

// ReplyToEventWithSeq 用 SeqAllocator 取号并设置 event_id 锚点（被动回复某个事件）。
func (m *Message) ReplyToEventWithSeq(ctx context.Context, a SeqAllocator, eventID string) (*Message, error) {
	if a == nil {
		return m, fmt.Errorf("send: SeqAllocator 不能为 nil")
	}
	seq, err := a.Next(ctx, eventID)
	if err != nil {
		return m, err
	}
	return m.ReplyToEvent(eventID, seq), nil
}
