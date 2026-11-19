package send

// seq_test.go 覆盖被动回复 msg_seq 取号：单调、锚点隔离、并发、内存实现边界。

import (
	"context"
	"sync"
	"testing"
)

func TestMemorySeqMonotonicPerAnchor(t *testing.T) {
	a := NewMemorySeq()
	ctx := context.Background()

	for want := 1; want <= 3; want++ {
		got, err := a.Next(ctx, "msg-1")
		if err != nil || got != want {
			t.Fatalf("Next(msg-1) = %d, %v; want %d", got, err, want)
		}
	}
	// 不同锚点独立计数：msg_id 与 event_id 不能共用计数器（这是协议约束）。
	if got, _ := a.Next(ctx, "ev-1"); got != 1 {
		t.Fatalf("Next(ev-1) = %d, want 1（锚点必须隔离）", got)
	}
	if got, _ := a.Next(ctx, "msg-1"); got != 4 {
		t.Fatalf("Next(msg-1) = %d, want 4", got)
	}
}

func TestMemorySeqConcurrent(t *testing.T) {
	a := NewMemorySeq()
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := a.Next(context.Background(), "same"); err != nil {
				t.Errorf("Next error = %v", err)
			}
		}()
	}
	wg.Wait()
	got, err := a.Next(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	if got != n+1 {
		t.Fatalf("并发后下一个 seq = %d, want %d（不能重复）", got, n+1)
	}
}

func TestSeqRejectsEmptyAnchorAndNil(t *testing.T) {
	if _, err := NewMemorySeq().Next(context.Background(), ""); err == nil {
		t.Fatal("空锚点应报错")
	}
	if _, err := Text("x").ReplyToWithSeq(context.Background(), nil, "m1"); err == nil {
		t.Fatal("nil SeqAllocator 应报错")
	}
	// ctx 已取消时不取号。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewMemorySeq().Next(ctx, "m1"); err == nil {
		t.Fatal("已取消 ctx 应报错")
	}
}

func TestReplyWithSeqHelpers(t *testing.T) {
	a := NewMemorySeq()
	msg, err := Text("hi").ReplyToWithSeq(context.Background(), a, "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if id, seq := msg.Reply(); id != "msg-1" || seq != 1 {
		t.Fatalf("ReplyToWithSeq = %q/%d", id, seq)
	}
	msg2, err := Markdown("hi").ReplyToEventWithSeq(context.Background(), a, "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	if id, seq := msg2.ReplyEvent(); id != "ev-1" || seq != 1 {
		t.Fatalf("ReplyToEventWithSeq = %q/%d", id, seq)
	}
	// 与 msg_id 的计数器互不影响。
	if id, seq := msg.Reply(); id != "msg-1" || seq != 1 {
		t.Fatalf("msg_id 计数器被 event_id 污染: %q/%d", id, seq)
	}
}
