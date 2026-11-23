package api

// retry_config_test.go 覆盖 RetryConfig 三项可配参数：MaxAttempts 关闭、Base 退避基数、MaxRetryAfter 上限。

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/token"
)

func TestRetryConfigDisabled(t *testing.T) {
	var attempts int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&attempts, 1)
			writeJSON(w, 429, `{"err_code":40034100,"message":"rate limited"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(time.Duration) {},
		Retry:    &RetryConfig{MaxAttempts: -1}, // 关闭 SDK 内重试
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("429 应返回错误")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("MaxAttempts<0 时应只尝试 1 次，实际 %d", got)
	}
}

func TestRetryConfigBaseAndMaxRetryAfter(t *testing.T) {
	var attempts int32
	var sleeps []time.Duration
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/users/@me":
			if atomic.AddInt32(&attempts, 1) < 3 {
				writeJSON(w, 500, `{"err_code":500,"message":"boom"}`)
				return
			}
			writeJSON(w, 200, `{"id":"bot","username":"b"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(d time.Duration) { sleeps = append(sleeps, d) },
		Retry:    &RetryConfig{MaxAttempts: 3, Base: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v, want 2 次（第 2/3 次尝试前）", sleeps)
	}
	// 抖动区间：base×(attempt-1) 的 [1.0, 1.2] 倍。
	if sleeps[0] < 50*time.Millisecond || sleeps[0] > 60*time.Millisecond {
		t.Fatalf("第 1 次退避 = %v, want [50ms,60ms]", sleeps[0])
	}
	if sleeps[1] < 100*time.Millisecond || sleeps[1] > 120*time.Millisecond {
		t.Fatalf("第 2 次退避 = %v, want [100ms,120ms]", sleeps[1])
	}
}

func TestRetryAfterCappedByConfig(t *testing.T) {
	var attempts int32
	var sleeps []time.Duration
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/users/@me":
			if atomic.AddInt32(&attempts, 1) == 1 {
				w.Header().Set("Retry-After", "120")
				writeJSON(w, 429, `{"err_code":40034100,"message":"rate limited"}`)
				return
			}
			writeJSON(w, 200, `{"id":"bot","username":"b"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(d time.Duration) { sleeps = append(sleeps, d) },
		Retry:    &RetryConfig{MaxRetryAfter: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 5*time.Second {
		t.Fatalf("sleeps = %v, want [5s]（Retry-After 120s 被上限截断）", sleeps)
	}
}
