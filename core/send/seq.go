package send

// seq.go 提供被动回复 msg_seq 的取号能力：按锚点（msg_id/event_id）分别单调递增。
// 生产环境的跨重启单调由宿主注入持久化实现，内存实现仅覆盖单进程与测试。

import (
	"context"
	"fmt"
	"sync"
)

// SeqAllocator 为被动回复分配 msg_seq（按 anchor 分别计数，并发安全由实现保证）。
type SeqAllocator interface {
	Next(ctx context.Context, anchor string) (int, error)
}

// maxMemorySeqKeys 是内存实现的锚点上限，超出按 LRU 淘汰。
const maxMemorySeqKeys = 4096

// maxSeqValue 是 msg_seq 的协议上限（int32 上限），到顶后报错不回绕。
const maxSeqValue = 1<<31 - 1

// memorySeq 是单进程内存实现（重启归零）。
type memorySeq struct {
	mu    sync.Mutex
	seqs  map[string]int
	order []string // 最近使用的排在末尾（LRU）
}

// NewMemorySeq 创建内存 SeqAllocator：进程内单调，重启后从 1 重新开始。
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
		if len(s.seqs) >= maxMemorySeqKeys {
			s.evictLRU()
		}
		s.seqs[anchor] = 0
		s.order = append(s.order, anchor)
	} else {
		s.touch(anchor)
	}
	if s.seqs[anchor] >= maxSeqValue {
		return 0, fmt.Errorf("send: 锚点 %q 的 msg_seq 已达上限 %d", anchor, maxSeqValue)
	}
	s.seqs[anchor]++
	return s.seqs[anchor], nil
}

// touch 把锚点移到队尾（最近使用）。
func (s *memorySeq) touch(anchor string) {
	for i, k := range s.order {
		if k == anchor {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.order = append(s.order, anchor)
}

// evictLRU 淘汰最久未使用的锚点（锚点被淘汰后 seq 从 1 重新计数）。
func (s *memorySeq) evictLRU() {
	for len(s.order) > 0 {
		old := s.order[0]
		s.order = s.order[1:]
		if _, ok := s.seqs[old]; ok {
			delete(s.seqs, old)
			return
		}
	}
	// order 异常（与 map 不同步）：兜底删任意一个，保证不会无限增长。
	for k := range s.seqs {
		delete(s.seqs, k)
		return
	}
}

// ReplyToWithSeq 用 SeqAllocator 取号并设置 msg_id 锚点（被动回复某条消息）。
func (m *Message) ReplyToWithSeq(ctx context.Context, a SeqAllocator, anchor string) (*Message, error) {
	return m.replyWith(ctx, a, anchor, m.ReplyTo)
}

// ReplyToEventWithSeq 用 SeqAllocator 取号并设置 event_id 锚点（被动回复某个事件）。
func (m *Message) ReplyToEventWithSeq(ctx context.Context, a SeqAllocator, anchor string) (*Message, error) {
	return m.replyWith(ctx, a, anchor, m.ReplyToEvent)
}

// replyWith 是两个 *WithSeq 方法的公共骨架：取号 + 落到指定锚点。
func (m *Message) replyWith(ctx context.Context, a SeqAllocator, anchor string, set func(string, int) *Message) (*Message, error) {
	if a == nil {
		return m, fmt.Errorf("send: SeqAllocator 不能为 nil")
	}
	seq, err := a.Next(ctx, anchor)
	if err != nil {
		return m, err
	}
	return set(anchor, seq), nil
}
