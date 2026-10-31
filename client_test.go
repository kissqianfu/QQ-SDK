package qqbot

import (
	"context"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
)

// fakeTransport 是测试用接入通道：发一条事件后阻塞到 ctx 取消。
type fakeTransport struct {
	sent bool
}

func (f *fakeTransport) Run(ctx context.Context, emit func(*event.Event) error) error {
	if err := emit(&event.Event{ID: "ev-1", Type: event.C2CMessageCreate}); err != nil {
		return err
	}
	f.sent = true
	<-ctx.Done()
	return nil
}

func (f *fakeTransport) Name() string { return "fake" }

// raceTransport 立即返回，制造 Run 收尾 defer 与 Close 的并发窗口。
type raceTransport struct{}

func (raceTransport) Run(context.Context, func(*event.Event) error) error { return nil }
func (raceTransport) Name() string                                        { return "race" }

// 回归：Close 与 Run 收尾并发时不得双重 close（曾 panic: close of closed channel）。
func TestCloseConcurrentWithRunExit(t *testing.T) {
	for i := 0; i < 200; i++ {
		c, err := New(Config{AppID: "app", AppSecret: "secret", Transport: raceTransport{}})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		done := make(chan struct{})
		go func() { _ = c.Run(context.Background()); close(done) }()
		go func() { _ = c.Close() }()
		<-done
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New(empty config) error = nil, want error")
	}
}

func TestRunDeliversEventsAndCloses(t *testing.T) {
	tr := &fakeTransport{}
	c, err := New(Config{AppID: "app", AppSecret: "secret", Transport: tr})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.Sender() == nil {
		t.Fatal("Sender() = nil")
	}

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()

	select {
	case ev := <-c.Events():
		if ev.ID != "ev-1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not exit after Close")
	}
	// Run 返回后通道必须关闭。
	if _, ok := <-c.Events(); ok {
		t.Fatal("Events() channel still open after Run returned")
	}
	// Close 幂等。
	if err := c.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestRunWithoutTransport(t *testing.T) {
	c, err := New(Config{AppID: "app", AppSecret: "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("Run(no transport) error = nil, want error")
	}
}

func TestRunTwiceRejected(t *testing.T) {
	tr := &fakeTransport{}
	c, err := New(Config{AppID: "app", AppSecret: "secret", Transport: tr})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()

	// 等第一个 Run 真正跑起来（收到事件说明 Transport.Run 已被调用）。
	select {
	case <-c.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("second Run() error = nil, want error")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not exit after Close")
	}
	if _, ok := <-c.Events(); ok {
		t.Fatal("Events() channel still open after Run returned")
	}
}

func TestCloseBeforeRunClosesEvents(t *testing.T) {
	tr := &fakeTransport{}
	c, err := New(Config{AppID: "app", AppSecret: "secret", Transport: tr})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// 未 Run 就 Close：事件通道立即关闭，消费者 range 直接结束而不是永久阻塞。
	if _, ok := <-c.Events(); ok {
		t.Fatal("Events() channel still open after Close without Run")
	}
	// Close 之后再 Run 应安全返回（通道已关闭，不能再跑）。
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run() after Close error = %v, want nil", err)
	}
	if tr.sent {
		t.Fatal("transport ran after Close")
	}
}

func TestClientMeUsesAPI(t *testing.T) {
	c, err := New(Config{AppID: "app", AppSecret: "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// 没有可用网络：Me 应返回错误而不是 panic。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Me(ctx); err == nil {
		t.Fatal("Me(canceled) error = nil, want error")
	}
}
