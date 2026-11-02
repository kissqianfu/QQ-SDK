package webhook

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
)

const testSecret = "test-secret-1234567890"

// newHandler 构造 Handler 并把事件收集到 channel。
func newHandler(t *testing.T, opts Options) (*Handler, ed25519.PrivateKey, <-chan *event.Event) {
	t.Helper()
	ch := make(chan *event.Event, 8)
	opts.Secret = testSecret
	if opts.OnEvent == nil {
		opts.OnEvent = func(_ context.Context, ev *event.Event) { ch <- ev }
	}
	h, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, testPrivateKey(), ch
}

// testPrivateKey 按官方协议把 testSecret repeat 到 32 字节，独立于 SDK 实现。
func testPrivateKey() ed25519.PrivateKey {
	seed := make([]byte, 0, ed25519.SeedSize)
	for len(seed) < ed25519.SeedSize {
		seed = append(seed, testSecret...)
	}
	return ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
}

// TestSeedRepeat 验证短 secret 不是零填充，而是按官方协议重复到 32 字节。
func TestSeedRepeat(t *testing.T) {
	h, err := New(Options{Secret: testSecret})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	want := testPrivateKey().Public().(ed25519.PublicKey)
	if !bytes.Equal(h.PublicKey(), want) {
		t.Fatalf("PublicKey 不一致：seed 未按官方协议 repeat")
	}
}

// signedRequest 造带合法签名的 POST 请求。
func signedRequest(t *testing.T, key ed25519.PrivateKey, body []byte) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	tsStr := strconv.FormatInt(ts, 10)
	msg := append([]byte(tsStr), body...)
	req := httptest.NewRequest(http.MethodPost, "/aichat/webhook", strings.NewReader(string(body)))
	req.Header.Set(SignatureHeader, hex.EncodeToString(ed25519.Sign(key, msg)))
	req.Header.Set(TimestampHeader, tsStr)
	return req
}

func TestChallenge(t *testing.T) {
	h, key, _ := newHandler(t, Options{})
	body := []byte(`{"op":13,"d":{"plain_token":"pt-1","event_ts":"1700000000"}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/aichat/webhook", strings.NewReader(string(body))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		PlainToken string `json:"plain_token"`
		Signature  string `json:"signature"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad challenge: %v", err)
	}
	sig, _ := hex.DecodeString(resp.Signature)
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte("1700000000pt-1"), sig) {
		t.Fatal("challenge signature invalid")
	}
}

func TestACKThenDispatch(t *testing.T) {
	h, key, events := newHandler(t, Options{})
	body := []byte(`{"id":"EV-1","op":0,"t":"C2C_MESSAGE_CREATE","d":{"id":"m1","content":"hi","author":{"user_openid":"u1"}}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, key, body))

	if rec.Code != http.StatusOK || rec.Body.String() != ackBody {
		t.Fatalf("ACK = %d %q", rec.Code, rec.Body.String())
	}
	select {
	case ev := <-events:
		if ev.ID != "EV-1" || ev.MsgID != "m1" || ev.TargetID != "u1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not dispatched")
	}
	if h.Acknowledged() != 1 {
		t.Fatalf("acked = %d", h.Acknowledged())
	}
}

func TestDuplicateAcksWithoutDispatch(t *testing.T) {
	h, key, events := newHandler(t, Options{
		Deduper: func(context.Context, EventMeta) (bool, error) { return false, nil },
	})
	body := []byte(`{"id":"EV-2","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, key, body))
	if rec.Code != http.StatusOK || rec.Body.String() != ackBody {
		t.Fatalf("ACK = %d %q", rec.Code, rec.Body.String())
	}
	select {
	case ev := <-events:
		t.Fatalf("duplicate dispatched: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDedupeErrorReturns500(t *testing.T) {
	h, key, _ := newHandler(t, Options{
		Deduper: func(context.Context, EventMeta) (bool, error) { return false, context.DeadlineExceeded },
	})
	body := []byte(`{"id":"EV-3","op":0,"t":"C2C_MESSAGE_CREATE","d":{}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, key, body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestOnEventPanicDoesNotCrash(t *testing.T) {
	h, key, _ := newHandler(t, Options{
		OnEvent: func(context.Context, *event.Event) { panic("boom") },
	})
	body := []byte(`{"id":"EV-9","op":0,"t":"C2C_MESSAGE_CREATE","d":{}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, key, body))
	// ACK 正常返回；worker 内 panic 被恢复，进程存活（随后续事件可继续处理）。
	if rec.Code != http.StatusOK || rec.Body.String() != ackBody {
		t.Fatalf("ACK = %d %q", rec.Code, rec.Body.String())
	}
}

func TestBadRequests(t *testing.T) {
	h, key, _ := newHandler(t, Options{AppID: "app-1"})

	body := []byte(`{"id":"EV-4","op":0,"t":"C2C_MESSAGE_CREATE","d":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/aichat/webhook", strings.NewReader(string(body)))
	req.Header.Set(SignatureHeader, "deadbeef")
	req.Header.Set(TimestampHeader, "1700000000")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig status = %d", rec.Code)
	}

	req = signedRequest(t, key, body)
	req.Header.Set(AppIDHeader, "other")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("appid mismatch status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/aichat/webhook", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/aichat/webhook", strings.NewReader(`{"op":7}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported op status = %d", rec.Code)
	}
}
