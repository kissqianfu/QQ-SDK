package api

// quota_refund_test.go 覆盖配额记账的退款语义：平台拒绝要退，平台已接收不退。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/send"
	"github.com/kissqianfu/QQ-SDK/core/token"
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
	rel := c.quota.relations[quotaKey(event.SceneGroup, "g1")]
	if rel == nil || rel.tokens != 1 {
		t.Fatalf("单关系桶未退回：%+v", rel)
	}
	if c.quota.daily[quotaKey(event.SceneGroup, "g1")] != 0 {
		t.Fatalf("失败不应计入日额度：%d", c.quota.daily[quotaKey(event.SceneGroup, "g1")])
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
