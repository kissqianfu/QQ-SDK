package qqbot

// lifecycle_extra_test.go 覆盖生命周期回归：
//   - Run 正常返回后再 Run：历史实现会向已关闭的 events 通道写入 → panic
//     （send on closed channel）；现在第二次 Run 直接返回 nil。
//   - Close 后 Run 仍返回 nil（既有契约，不能因上面的修复而破坏）。

import (
	"context"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
)

// runThenExitTransport 只发一个事件就返回，用来让 Run 走完收尾路径。
type runThenExitTransport struct{}

func (runThenExitTransport) Name() string { return "run-then-exit" }

func (runThenExitTransport) Run(ctx context.Context, emit func(*event.Event) error) error {
	if err := emit(&event.Event{ID: "e1"}); err != nil {
		return err
	}
	return nil
}

func TestRunTwiceDoesNotPanic(t *testing.T) {
	c, err := New(Config{AppID: "app", AppSecret: "secret", Transport: runThenExitTransport{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	// 第二次 Run 不能在已关闭的 events 上发送；历史实现这里 panic。
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Run() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Run() 卡住")
	}
	// 通道必须已关闭，range 能正常结束（第一条 Run 的事件还在缓冲里，允许先读到它）。
	got := 0
	for range c.Events() {
		got++
	}
	if got != 1 {
		t.Fatalf("drained %d events, want 1", got)
	}
}

func TestRunAfterCloseStillNil(t *testing.T) {
	c, err := New(Config{AppID: "app", AppSecret: "secret", Transport: runThenExitTransport{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run() after Close error = %v, want nil", err)
	}
}

// TestRunWithoutTransportClosesEvents：没有 Transport 时 Run 立即失败，
// 但事件通道必须同时关闭，否则 range Events() 的消费者会永久阻塞。
func TestRunWithoutTransportClosesEvents(t *testing.T) {
	c, err := New(Config{AppID: "app", AppSecret: "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("Run(no transport) error = nil, want error")
	}
	done := make(chan struct{})
	go func() {
		for range c.Events() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 早退后 Events() 未关闭（消费者被永久阻塞）")
	}
}
