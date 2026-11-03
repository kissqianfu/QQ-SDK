package api

// retry_auth_test.go 锁定重试与鉴权失效策略（code-audit 复现项）：
//  1. 非幂等 POST 遇 5xx 默认不重发（避免重复消息）；
//  2. 显式 Config.RetryNonIdempotent=true 时恢复旧行为；
//  3. 429 对 POST 仍重试（请求肯定没被处理）；
//  4. 401 后若 token 存储实现 Deleter，则作废缓存、换新 token 并重试一次。

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

func TestPost5xxNotRetriedByDefault(t *testing.T) {
	var attempts int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&attempts, 1)
			writeJSON(w, 500, `{"err_code":500,"message":"boom"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1))
	if err == nil {
		t.Fatal("Send() error = nil, want error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("POST 5xx 被重发 %d 次，want 1（非幂等不重试）", got)
	}
}

func TestPost5xxRetriedWhenOptedIn(t *testing.T) {
	var attempts int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			n := atomic.AddInt32(&attempts, 1)
			if n < 3 {
				writeJSON(w, 503, `{"err_code":503,"message":"unavailable"}`)
				return
			}
			writeJSON(w, 200, `{"id":"m1","timestamp":"2026-07-21T10:30:00+08:00"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:            srv.URL,
		TokenURL:           srv.URL + "/app/getAppAccessToken",
		Tokens:             token.NewMemory(),
		Sleep:              func(time.Duration) {},
		RetryNonIdempotent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestPost429StillRetried(t *testing.T) {
	var attempts int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			if atomic.AddInt32(&attempts, 1) == 1 {
				writeJSON(w, 429, `{"err_code":40034100,"message":"rate limited"}`)
				return
			}
			writeJSON(w, 200, `{"id":"m1"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2（429 重试）", got)
	}
}

func TestUnauthorizedRefreshesTokenOnce(t *testing.T) {
	var tokenCalls, msgCalls int32
	var lastAuth string
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			n := atomic.AddInt32(&tokenCalls, 1)
			tok := "t1"
			if n > 1 {
				tok = "t2"
			}
			writeJSON(w, 200, `{"access_token":"`+tok+`","expires_in":7200}`)
		default:
			atomic.AddInt32(&msgCalls, 1)
			lastAuth = r.Header.Get("Authorization")
			if lastAuth != "QQBot t2" {
				writeJSON(w, 401, `{"err_code":11244,"message":"token invalid"}`)
				return
			}
			writeJSON(w, 200, `{"id":"m1"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1)); err != nil {
		t.Fatalf("Send() error = %v（401 后应换 token 重试一次）", err)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 2 {
		t.Fatalf("token 刷新 %d 次, want 2", got)
	}
	if got := atomic.LoadInt32(&msgCalls); got != 2 {
		t.Fatalf("业务请求 %d 次, want 2", got)
	}
	if lastAuth != "QQBot t2" {
		t.Fatalf("最后一次 Authorization = %q", lastAuth)
	}
}

func TestUnauthorizedWithoutDeleterReturnsError(t *testing.T) {
	var msgCalls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&msgCalls, 1)
			writeJSON(w, 401, `{"err_code":11244,"message":"token invalid"}`)
		}
	})
	// fakeStore 只实现 Store（不实现 Deleter）→ 不能失效缓存，直接返回 401 错误。
	c := newClient(t, srv, &fakeStore{tok: "tok"})
	_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1))
	if err == nil {
		t.Fatal("Send() error = nil, want 401 error")
	}
	if got := atomic.LoadInt32(&msgCalls); got != 1 {
		t.Fatalf("msg calls = %d, want 1（无 Deleter 不重试）", got)
	}
}
