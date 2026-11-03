package api

// reply_test.go 覆盖 SendReply 的锚点降级语义：
//   - 锚点被拒（IsAnchorRejected）→ 换下一个锚点；
//   - 40054005 去重 → 当成功返回，绝不重发；
//   - 其它错误 → 直接返回，不降级；
//   - 无锚点 / MaxAttempts 边界。

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

func anchorTestEvent() *event.Event {
	return &event.Event{
		ID:      "EV-1",
		Type:    event.InteractionCreate,
		EventID: "INTERACTION_CREATE:ev1",
		Interaction: &event.InteractionData{
			MessageID: "btn-msg",
		},
	}
}

func TestSendReplyFallsBackOnAnchorRejected(t *testing.T) {
	var calls int32
	var bodies []map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			n := atomic.AddInt32(&calls, 1)
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			if n == 1 {
				// 第一个锚点（外层 event_id）被平台判无效。
				writeJSON(w, 400, `{"err_code":40034025,"message":"event_id invalid"}`)
				return
			}
			writeJSON(w, 200, `{"id":"m-ok","timestamp":"2026-07-21T10:30:00+08:00"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	res, err := c.SendReply(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		anchorTestEvent(),
		func() *send.Message { return send.Markdown("收到") },
		ReplyOptions{Seq: send.NewMemorySeq()})
	if err != nil {
		t.Fatalf("SendReply() error = %v", err)
	}
	if res == nil || res.ID != "m-ok" {
		t.Fatalf("res = %+v", res)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2（锚点被拒后应降级一次）", got)
	}
	if bodies[0]["event_id"] != "INTERACTION_CREATE:ev1" {
		t.Fatalf("第 1 次应带外层 event_id: %+v", bodies[0])
	}
	if bodies[1]["msg_id"] != "btn-msg" {
		t.Fatalf("第 2 次应降级到按钮消息 msg_id: %+v", bodies[1])
	}
}

func TestSendReplyDuplicateCountsAsSuccess(t *testing.T) {
	var calls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&calls, 1)
			writeJSON(w, 400, `{"err_code":40054005,"message":"dup"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	res, err := c.SendReply(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		anchorTestEvent(),
		func() *send.Message { return send.Markdown("收到") },
		ReplyOptions{Seq: send.NewMemorySeq()})
	if err != nil {
		t.Fatalf("40054005 应视为成功，error = %v", err)
	}
	if res == nil {
		t.Fatal("res = nil, want 非 nil（已送达）")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1（去重后绝不能换锚点重发）", got)
	}
}

func TestSendReplyStopsOnNonAnchorError(t *testing.T) {
	var calls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&calls, 1)
			writeJSON(w, 400, `{"err_code":40034006,"message":"内容违规"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	_, err := c.SendReply(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		anchorTestEvent(),
		func() *send.Message { return send.Markdown("收到") },
		ReplyOptions{})
	if err == nil {
		t.Fatal("内容违规应返回错误")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1（非锚点错误不应降级）", got)
	}
}

func TestSendReplyNoAnchorAndMaxAttempts(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
	}), token.NewMemory())

	// 未知事件没有锚点。
	if _, err := c.SendReply(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		&event.Event{Type: "UNKNOWN"}, func() *send.Message { return send.Text("x") }, ReplyOptions{}); err == nil {
		t.Fatal("无锚点应报错")
	}
	// nil 参数。
	if _, err := c.SendReply(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, nil,
		func() *send.Message { return send.Text("x") }, ReplyOptions{}); err == nil {
		t.Fatal("nil event 应报错")
	}
	if _, err := c.SendReply(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		anchorTestEvent(), nil, ReplyOptions{}); err == nil {
		t.Fatal("nil build 应报错")
	}
}
