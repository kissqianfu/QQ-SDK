package qqbot

// ingress_test.go 覆盖门面内置双通道（方案 B）：
// 配置校验、ws 注入、webhook 事件投递、满载关闭不死锁。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/transport/webhook"
	"github.com/kissqianfu/QQ-SDK/transport/ws"
)

func TestConfigModeValidation(t *testing.T) {
	base := Config{AppID: "app", AppSecret: "secret"}
	cases := []struct {
		name string
		cfg  Config
	}{
		{"非法 Mode", func() Config { c := base; c.Mode = "mqtt"; return c }()},
		{"Mode 与 Transport 互斥", func() Config {
			c := base
			c.Mode = ModeWS
			c.Transport = &fakeTransport{}
			return c
		}()},
		{"空 Mode 却传 WSOptions", func() Config { c := base; c.WSOptions = &ws.Options{}; return c }()},
		{"ws 模式却传 WebhookOptions", func() Config {
			c := base
			c.Mode = ModeWS
			c.WebhookOptions = &webhook.Options{}
			return c
		}()},
		{"webhook 模式却传 WSOptions", func() Config {
			c := base
			c.Mode = ModeWebhook
			c.WSOptions = &ws.Options{}
			return c
		}()},
		{"webhook 自行设置 OnEvent", func() Config {
			c := base
			c.Mode = ModeWebhook
			c.WebhookOptions = &webhook.Options{OnEvent: func(context.Context, *event.Event) {}}
			return c
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatalf("Validate(%s) = nil, want error", tc.name)
			}
		})
	}

	// 合法组合。
	valid := []Config{
		base,
		func() Config { c := base; c.Mode = ModeWS; return c }(),
		func() Config { c := base; c.Mode = ModeWebhook; return c }(),
	}
	for i, cfg := range valid {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid[%d] Validate() = %v", i, err)
		}
	}
}

func TestModeWSBuildsTransport(t *testing.T) {
	customIntents := ws.IntentGroupAndC2CEvent | ws.IntentInteraction
	opts := &ws.Options{Intents: customIntents}
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		Mode:      ModeWS,
		WSOptions: opts,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.cfg.Transport == nil || c.cfg.Transport.Name() != "websocket" {
		t.Fatalf("Mode=ws 未装配 transport: %+v", c.cfg.Transport)
	}
	if c.WebhookHTTPHandler() != nil {
		t.Fatal("ws 模式不应有 webhook handler")
	}
	// 值拷贝：门面回填（Token/SessionStore/Logger/AppID）不能写回调用方的结构体。
	if opts.Token != nil {
		t.Fatal("WSOptions 被门面回填，破坏了值拷贝约定")
	}
	if opts.SessionStore != nil {
		t.Fatal("WSOptions.SessionStore 被门面写回")
	}
	if opts.AppID != "" {
		t.Fatal("WSOptions.AppID 被门面写回")
	}
}

func TestWebhookHTTPHandlerNilWithoutMode(t *testing.T) {
	c, err := New(Config{AppID: "app", AppSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if c.WebhookHTTPHandler() != nil {
		t.Fatal("未配置 Mode 时 WebhookHTTPHandler 应为 nil")
	}
}

// signedWebhookRequest 造一条带合法签名的 op0 回调请求（与 webhook 包内部算法一致）。
func signedWebhookRequest(t *testing.T, secret string, body []byte) *http.Request {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = secret[i%len(secret)]
	}
	key := ed25519.NewKeyFromSeed(seed)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(key, append([]byte(ts), body...))
	req := httptest.NewRequest(http.MethodPost, "/callback", bytes.NewReader(body))
	req.Header.Set(webhook.SignatureHeader, hex.EncodeToString(sig))
	req.Header.Set(webhook.TimestampHeader, ts)
	return req
}

func TestModeWebhookDeliversEvents(t *testing.T) {
	secret := "test-secret"
	c, err := New(Config{AppID: "app", AppSecret: secret, Mode: ModeWebhook})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	h := c.WebhookHTTPHandler()
	if h == nil {
		t.Fatal("webhook 模式 WebhookHTTPHandler() = nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(ctx) }()

	body := []byte(`{"id":"EV-1","op":0,"t":"FRIEND_ADD","d":{"openid":"u1"}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedWebhookRequest(t, secret, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook status = %d", rec.Code)
	}

	select {
	case ev := <-c.Events():
		if ev.ID != "EV-1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("事件未从 Events() 出来")
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在取消后返回")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestWebhookShutdownUnderBackpressure 是方案 B 的关键回归：
// 事件缓冲区被灌满（消费者不读）后取消 Run，必须在有限时间内返回——
// 旧方案（先 h.Close 再排水）在这里会死锁。
func TestWebhookShutdownUnderBackpressure(t *testing.T) {
	secret := "test-secret"
	c, err := New(Config{
		AppID: "app", AppSecret: secret, Mode: ModeWebhook,
		WebhookOptions: &webhook.Options{QueueSize: 512, Workers: 8},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	h := c.WebhookHTTPHandler()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(ctx) }()

	// 灌入远超 eventBuffer(128) 的事件，且不消费 Events()：
	// worker 会阻塞在 pushEvent，触发反压/丢弃路径。
	total := eventBuffer + 200
	for i := 0; i < total; i++ {
		body := []byte(fmt.Sprintf(`{"id":"EV-%d","op":0,"t":"FRIEND_ADD","d":{"openid":"u1"}}`, i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedWebhookRequest(t, secret, body))
		if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("第 %d 条 webhook status = %d", i, rec.Code)
		}
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("满载关闭时 Run 未返回：疑似死锁")
	}

	// Close 也必须能返回（幂等）。
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() 未返回")
	}
}
