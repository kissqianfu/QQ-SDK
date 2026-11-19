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
