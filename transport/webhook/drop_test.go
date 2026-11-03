package webhook

// drop_test.go 覆盖"ACK 后队列满丢弃"的可观测性：必须回调 OnDrop（宿主据此落死信/告警），
// 而不是只留一条 Warn 日志。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
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

	post := func(id string) {
		t.Helper()
		body := []byte(`{"id":"` + id + `","op":0,"t":"FRIEND_ADD","d":{"openid":"u1"}}`)
		req := signedRequest(t, key, body)
		rec := serve(h, body, map[string]string{
			SignatureHeader: req.Header.Get(SignatureHeader),
			TimestampHeader: req.Header.Get(TimestampHeader),
		})
		if rec.Code != 200 {
			t.Fatalf("%s status = %d, body = %s", id, rec.Code, rec.Body.String())
		}
	}

	post("EV-1")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始处理第一个事件")
	}
	post("EV-2") // 占满 QueueSize=1
	post("EV-3") // 必然丢弃
	post("EV-4") // 必然丢弃

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
	close(block)
}
