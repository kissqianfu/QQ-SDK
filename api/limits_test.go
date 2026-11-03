package api

// limits_test.go 覆盖响应体上限与"日额度只计成功"两条边界。

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

func TestResponseBodyTooLargeIsExplicitError(t *testing.T) {
	big := strings.Repeat("x", maxBody+1)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(big))
		}
	})
	c := newClient(t, srv, token.NewMemory())
	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("Me() error = nil，want 响应体超限错误")
	}
	if !strings.Contains(err.Error(), "响应体超过上限") {
		t.Fatalf("err = %v，want 明确的超限错误（不能静默截断成 JSON 解析失败）", err)
	}
}

func TestQuotaDailyOnlyCountsSuccessfulSends(t *testing.T) {
	var msgCalls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			atomic.AddInt32(&msgCalls, 1)
			// 平台侧拒绝：这条不算"成功发出"，不该占当日额度。
			writeJSON(w, 400, `{"err_code":40034006,"message":"消息内容违规"}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Quota:    &QuotaConfig{DailyPerTarget: 1}, // 日上限 1 条
	})
	if err != nil {
		t.Fatal(err)
	}
	// 连续失败 3 次：若失败也计数，第 2 次就会被本地日上限拦住不发请求。
	for i := 0; i < 3; i++ {
		_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
			send.Markdown("hi").Seq(i+1))
		if err == nil {
			t.Fatalf("第 %d 次 Send 应返回平台错误", i+1)
		}
	}
	if got := atomic.LoadInt32(&msgCalls); got != 3 {
		t.Fatalf("平台请求数 = %d，want 3（失败不应消耗日额度）", got)
	}
	if got := c.QuotaStats().Daily; got != 0 {
		t.Fatalf("Daily 拒绝数 = %d，want 0", got)
	}
}
