package webhook

// drop_test.go 覆盖队列满丢弃的可观测性：必须回调 OnDrop 而不只是 Warn 日志。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

func TestQueueFullCallsOnDrop(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	var mu sync.Mutex
	var dropped []EventMeta

	h, key, _ := newHandler(t, Options{
		QueueSize: 1,
		Workers:   1,
		OnEvent: func(_ context.Context, _ *event.Event) {
			started <- struct{}{}
			<-block
		},
		OnDrop: func(meta EventMeta) {
			mu.Lock()
			dropped = append(dropped, meta)
			mu.Unlock()
		},
	})

	post := func(id string) int {
		t.Helper()
		body := []byte(`{"id":"` + id + `","op":0,"t":"FRIEND_ADD","d":{"openid":"u1"}}`)
		req := signedRequest(t, key, body)
		rec := serve(h, body, map[string]string{
			SignatureHeader: req.Header.Get(SignatureHeader),
			TimestampHeader: req.Header.Get(TimestampHeader),
		})
		return rec.Code
	}

	if code := post("EV-1"); code != 200 {
		t.Fatalf("EV-1 status = %d", code)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始处理第一个事件")
	}
	if code := post("EV-2"); code != 200 { // 占满 QueueSize=1
		t.Fatalf("EV-2 status = %d", code)
	}
	// 队列满必须回 503（不 ACK），让平台重推；ACK 后丢弃是静默丢事件。
	for _, id := range []string{"EV-3", "EV-4"} {
		if code := post(id); code != 503 {
			t.Fatalf("%s status = %d, want 503", id, code)
		}
	}

	mu.Lock()
	got := append([]EventMeta(nil), dropped...)
	mu.Unlock()
	if len(got) < 1 {
		t.Fatalf("OnDrop 未被调用（dropped=%v）", got)
	}
	if got[0].Type != "FRIEND_ADD" || got[0].ID == "" {
		t.Fatalf("OnDrop meta = %+v，want 带 type/id", got[0])
	}
	if h.Dropped() < 1 {
		t.Fatalf("Dropped() = %d，want >=1", h.Dropped())
	}
	// 按类型统计：按钮回调被丢和普通事件被丢的严重程度不同，必须能区分。
	byType := h.DroppedByType()
	if byType["FRIEND_ADD"] < 1 {
		t.Fatalf("DroppedByType() = %v，want FRIEND_ADD>=1", byType)
	}
	var sum int64
	for _, n := range byType {
		sum += n
	}
	if sum != h.Dropped() {
		t.Fatalf("分类型合计 %d != Dropped() %d", sum, h.Dropped())
	}
	close(block)
}
