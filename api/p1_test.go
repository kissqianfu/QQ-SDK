package api

// p1_test.go 覆盖评审清单的 P1 项：配额退款、重试可配、正文长度本地拦截。

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

// TestQuotaRefundOnFailedSend：主动消息失败（平台 4xx）后，两层令牌都要退回、日计数不增加。
func TestQuotaRefundOnFailedSend(t *testing.T) {
	fixed := time.Unix(1700000000, 0)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			writeJSON(w, 400, `{"err_code":40034006,"message":"消息内容违规"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(time.Duration) {},
		Quota: &QuotaConfig{
			GroupQPM: 2, SingleQPS: 2, RelationQPM: 1, DailyPerTarget: 10,
			Now: func() time.Time { return fixed },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeGroup := c.quota.group.tokens
	_, err = c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").Seq(1))
	if err == nil {
		t.Fatal("平台 400 应返回错误")
	}
	if c.quota.group.tokens != beforeGroup {
		t.Fatalf("全局桶令牌未退回：%.1f → %.1f", beforeGroup, c.quota.group.tokens)
	}
	rel := c.quota.relations["g1"]
	if rel == nil || rel.tokens != 1 {
		t.Fatalf("单关系桶未退回：%+v", rel)
	}
	if c.quota.daily["g1"] != 0 {
		t.Fatalf("失败不应计入日额度：%d", c.quota.daily["g1"])
	}
}

// TestQuotaNotRefundedAfterPlatformAccepted：HTTP 2xx 后即便响应体解析失败也不退（消息已送达）。
func TestQuotaNotRefundedAfterPlatformAccepted(t *testing.T) {
	fixed := time.Unix(1700000000, 0)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			// 200 但不是合法 JSON：模拟"平台已接受、响应体不可解析"。
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`not-json`))
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(time.Duration) {},
		Quota: &QuotaConfig{
			GroupQPM: 2, SingleQPS: 2, RelationQPM: 1, DailyPerTarget: 10,
			Now: func() time.Time { return fixed },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").Seq(1)); err == nil {
		t.Fatal("非法响应体应返回解析错误")
	}
	if c.quota.group.tokens != 1 {
		t.Fatalf("已送达（2xx）不应退令牌：tokens=%.1f, want 1", c.quota.group.tokens)
	}
}

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
