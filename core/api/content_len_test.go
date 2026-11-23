package api

// content_len_test.go 覆盖 MaxContentLen 本地拦截：超长不发请求，边界值放行。

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/send"
	"github.com/kissqianfu/QQ-SDK/core/token"
)

func TestMaxContentLenLocalReject(t *testing.T) {
	var msgCalls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&msgCalls, 1)
			writeJSON(w, 200, `{"id":"m1"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:       srv.URL,
		TokenURL:      srv.URL + "/app/getAppAccessToken",
		Tokens:        token.NewMemory(),
		MaxContentLen: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 6 个 rune > 5：本地拒绝，且一个请求都不发（连 token 都不取）。
	_, err = c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("一二三四五六").ReplyTo("m1", 1))
	if err == nil || !strings.Contains(err.Error(), "MaxContentLen") {
		t.Fatalf("超长正文 error = %v，want 本地长度错误", err)
	}
	if got := atomic.LoadInt32(&msgCalls); got != 0 {
		t.Fatalf("本地拒绝仍发了 %d 个请求", got)
	}
	// 恰好 5 个 rune 放行（边界）。
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("一二三四五").ReplyTo("m1", 1)); err != nil {
		t.Fatalf("边界长度应放行: %v", err)
	}
	if got := atomic.LoadInt32(&msgCalls); got != 1 {
		t.Fatalf("msgCalls = %d, want 1", got)
	}
}
