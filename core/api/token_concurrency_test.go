package api

// token_concurrency_test.go 锁定 token 刷新的 singleflight：并发首启只打一次平台。

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/common/token"
)

func TestTokenSingleflightConcurrent(t *testing.T) {
	var tokenCalls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		atomic.AddInt32(&tokenCalls, 1)
		// 故意放慢：让并发调用都撞进"刷新中"的等待分支。
		time.Sleep(30 * time.Millisecond)
		writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
	})
	c := newClient(t, srv, token.NewMemory())

	const goroutines = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tok, err := c.Token(context.Background())
			if err != nil || tok != "tok" {
				failures.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Fatalf("%d 个并发调用失败", got)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("token 请求 %d 次，want 1（singleflight 失效）", got)
	}
}

// 刷新期 panic 必须变成错误交给调用方。未命名返回值时 recover 里的赋值会被丢弃，
// 结果是 (空 token, nil)：leader 发空票被 4004 断开，等待方却拿到真实 panic 错误。
func TestTokenRefreshPanicReturnsError(t *testing.T) {
	c, err := New(Config{
		AppID:     "app",
		AppSecret: "secret",
		Tokens:    token.NewMemory(),
		HTTP:      &http.Client{Transport: panicRoundTripper{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tok, err := c.Token(context.Background())
	if err == nil {
		t.Fatalf("刷新 panic 后必须返回错误，实际 tok=%q err=nil", tok)
	}
	if tok != "" {
		t.Fatalf("刷新 panic 后 token 必须为空，实际 %q", tok)
	}
	c.mu.Lock()
	inflight := c.inflight != nil
	c.mu.Unlock()
	if inflight {
		t.Fatal("panic 后 inflight 未清空，后续调用会卡在等待上一次 flight")
	}
}

// panicRoundTripper 模拟传输层 panic（如自定义 RoundTripper 的 bug）。
type panicRoundTripper struct{}

func (panicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("boom: 模拟传输层 panic")
}

// 缓存命中后不应再打平台：重复调用必须直接返回内存里的 token。
func TestTokenCacheHitNoRequest(t *testing.T) {
	var tokenCalls int32
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
	})
	c := newClient(t, srv, token.NewMemory())
	for range 3 {
		if _, err := c.Token(context.Background()); err != nil {
			t.Fatalf("Token() error = %v", err)
		}
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("token 请求 %d 次，want 1（缓存未命中）", got)
	}
}
