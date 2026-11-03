package ws

// session_memory_test.go 覆盖单进程 SessionStore。

import (
	"context"
	"testing"
)

func TestMemorySession(t *testing.T) {
	ctx := context.Background()
	s := NewMemorySession()

	// 初始无 session。
	if _, ok, err := s.Load(ctx); err != nil || ok {
		t.Fatalf("空 Load = %v, %v; want false, nil", ok, err)
	}
	// 空 SessionID 不视为有效会话。
	if err := s.Save(ctx, Session{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Load(ctx); ok {
		t.Fatal("空 SessionID 不应视为可 Resume")
	}
	// 正常保存/读取。
	if err := s.Save(ctx, Session{SessionID: "s1", Seq: 42}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load(ctx)
	if err != nil || !ok || got.SessionID != "s1" || got.Seq != 42 {
		t.Fatalf("Load = %+v, %v, %v", got, ok, err)
	}
	// Clear。
	if err := s.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Load(ctx); ok {
		t.Fatal("Clear 后仍可 Load")
	}
}

func TestMemorySessionContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewMemorySession()
	if _, _, err := s.Load(ctx); err == nil {
		t.Fatal("Load(canceled) 应返回错误")
	}
	if err := s.Save(ctx, Session{SessionID: "x"}); err == nil {
		t.Fatal("Save(canceled) 应返回错误")
	}
	if err := s.Clear(ctx); err == nil {
		t.Fatal("Clear(canceled) 应返回错误")
	}
}

// 类型断言：内存实现必须满足 SessionStore。
var _ SessionStore = (*MemorySession)(nil)
