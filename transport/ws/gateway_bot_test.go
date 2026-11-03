package ws

// gateway_bot_test.go 覆盖 GET /gateway/bot（分片建议 + session 限额）：
// 默认走 /gateway 时行为不变；GatewayBot=true 时用 endpoint+"/bot" 并对分片建议打 Warn。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kissqianfu/QQ-SDK/event"
)

func TestGetGatewayBot(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"wss://gateway.example/ws","shards":4,"session_start_limit":{"total":1000,"remaining":998,"reset_after":60000,"max_concurrency":1}}`))
	}))
	defer srv.Close()

	tr, err := New(Options{
		AppID:           "app",
		Token:           func(context.Context) (string, error) { return "tok", nil },
		GatewayEndpoint: srv.URL + "/gateway",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	info, err := tr.GatewayBot(context.Background())
	if err != nil {
		t.Fatalf("GatewayBot() error = %v", err)
	}
	if gotPath != "/gateway/bot" {
		t.Fatalf("path = %q, want /gateway/bot", gotPath)
	}
	if gotAuth != "QQBot tok" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if info.URL != "wss://gateway.example/ws" || info.Shards != 4 {
		t.Fatalf("info = %+v", info)
	}
	if info.SessionStartLimit.Remaining != 998 || info.SessionStartLimit.ResetAfter != 60000 {
		t.Fatalf("session limit = %+v", info.SessionStartLimit)
	}
}

// TestConnectOnceUsesGatewayBot 验证 GatewayBot=true 时连接流程走 /gateway/bot。
func TestConnectOnceUsesGatewayBot(t *testing.T) {
	gatewayHit := false
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gateway/bot" {
			gatewayHit = true
			_, _ = w.Write([]byte(`{"url":"` + "ws://" + r.Host + `/ws","shards":2}`))
			return
		}
		if r.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			// 发 Hello 后马上以可重连码关闭，让 connectOnce 退出。
			_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(4009, "expired"))
			time.Sleep(50 * time.Millisecond)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	tr, err := New(Options{
		AppID:           "app",
		Token:           func(context.Context) (string, error) { return "tok", nil },
		GatewayEndpoint: srv.URL + "/gateway",
		GatewayBot:      true,
		StaleTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = tr.connectOnce(context.Background(), func(*event.Event) error { return nil })
	if err == nil {
		t.Fatal("connectOnce() error = nil, want 连接被关闭返回错误")
	}
	if !gatewayHit {
		t.Fatal("GatewayBot=true 时没有请求 /gateway/bot")
	}
}
