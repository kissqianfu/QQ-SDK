package token

import (
	"context"
	"testing"
	"time"
)

func TestMemoryTokenStore(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()

	if _, ok, err := store.Get(ctx); err != nil || ok {
		t.Fatalf("empty Get() = ok=%v err=%v, want false/nil", ok, err)
	}

	if err := store.Set(ctx, "tok-1", time.Hour); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	tok, ok, err := store.Get(ctx)
	if err != nil || !ok || tok != "tok-1" {
		t.Fatalf("Get() = %q/%v/%v, want tok-1/true/nil", tok, ok, err)
	}
}

func TestMemoryTokenExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()

	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }

	if err := store.Set(ctx, "tok", 90*time.Second); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	now = now.Add(89 * time.Second)
	if _, ok, _ := store.Get(ctx); !ok {
		t.Fatal("token expired too early")
	}
	now = now.Add(2 * time.Second) // 共 91s
	if _, ok, _ := store.Get(ctx); ok {
		t.Fatal("token should be expired")
	}
}

func TestMemoryTokenCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := NewMemory().Get(ctx); err == nil {
		t.Fatal("Get(canceled) error = nil, want error")
	}
	if err := NewMemory().Set(ctx, "x", time.Minute); err == nil {
		t.Fatal("Set(canceled) error = nil, want error")
	}
}

// Delete 是 401 换票链路的一环：删掉之后必须立刻 Get 不到，
// 否则 api 层"失效缓存再取新票"会拿回同一个已失效的 token。
func TestMemoryDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	if err := store.Set(ctx, "tok-1", time.Hour); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if tok, ok, err := store.Get(ctx); err != nil || ok || tok != "" {
		t.Fatalf("Delete 后 Get() = %q, ok=%v, err=%v, want 空/false/nil", tok, ok, err)
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("重复 Delete() error = %v", err)
	}
}

// ctx 已取消时所有方法都要立刻返回错误，不能写也不能读（避免请求超时后还污染缓存）。
func TestMemoryCtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemory()
	if _, _, err := store.Get(ctx); err == nil {
		t.Fatal("Get(canceled) 应返回错误")
	}
	if err := store.Set(ctx, "tok", time.Hour); err == nil {
		t.Fatal("Set(canceled) 应返回错误")
	}
	if err := store.Delete(ctx); err == nil {
		t.Fatal("Delete(canceled) 应返回错误")
	}
}

// 零值 Memory（绕过 NewMemory）也要能工作：clock 为 nil 时回落 time.Now，不能空指针崩。
func TestMemoryZeroValue(t *testing.T) {
	ctx := context.Background()
	var store Memory
	if err := store.Set(ctx, "tok-1", time.Hour); err != nil {
		t.Fatalf("零值 Set() error = %v", err)
	}
	if tok, ok, err := store.Get(ctx); err != nil || !ok || tok != "tok-1" {
		t.Fatalf("零值 Get() = %q, ok=%v, err=%v", tok, ok, err)
	}
}
