package webhook

// lifecycle_test.go 覆盖 Handler 生命周期与处理超时：
//   - Close 之后新请求返回 503（不 ACK，让平台重推，不静默丢）；
//   - Close 会等待已入队事件处理完（worker 回收）；
//   - OnEventTimeout 会把带超时的 ctx 传给 OnEvent。

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
)

func TestCloseStopsAcceptingAndDrains(t *testing.T) {
	var processed atomic.Int64
	h, key, _ := newHandler(t, Options{
		OnEvent: func(context.Context, *event.Event) { processed.Add(1) },
	})

	body := []byte(`{"id":"EV-1","op":0,"t":"FRIEND_ADD","d":{"openid":"u1"}}`)
	req := signedRequest(t, key, body)
	rec := serve(h, body, map[string]string{
		SignatureHeader: req.Header.Get(SignatureHeader),
		TimestampHeader: req.Header.Get(TimestampHeader),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("close 前 status = %d", rec.Code)
	}

	h.Close()
	// 幂等。
	h.Close()

	// 已入队事件必须处理完（Close 内部 wg.Wait）。
	if got := processed.Load(); got != 1 {
		t.Fatalf("processed = %d, want 1（Close 应处理完已入队事件）", got)
	}

	// Close 之后的新请求：503（不 ACK，平台会重推）。
	req2 := signedRequest(t, key, body)
	ts := time.Now().Unix()
	rec2 := serve(h, body, map[string]string{
		"X-Signature-Ed25519":   req2.Header.Get(SignatureHeader),
		"X-Signature-Timestamp": strconv.FormatInt(ts, 10),
	})
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("close 后 status = %d, want 503", rec2.Code)
	}
}

func TestOnEventTimeoutPropagates(t *testing.T) {
	gotDeadline := make(chan bool, 1)
	h, key, _ := newHandler(t, Options{
		OnEventTimeout: 100 * time.Millisecond,
		OnEvent: func(ctx context.Context, _ *event.Event) {
			_, ok := ctx.Deadline()
			gotDeadline <- ok
		},
	})
	body := []byte(`{"id":"EV-2","op":0,"t":"FRIEND_ADD","d":{"openid":"u1"}}`)
	req := signedRequest(t, key, body)
	rec := serve(h, body, map[string]string{
		SignatureHeader: req.Header.Get(SignatureHeader),
		TimestampHeader: req.Header.Get(TimestampHeader),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	select {
	case ok := <-gotDeadline:
		if !ok {
			t.Fatal("OnEvent 收到的 ctx 没有 deadline（OnEventTimeout 未生效）")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnEvent 未被调用")
	}
}
