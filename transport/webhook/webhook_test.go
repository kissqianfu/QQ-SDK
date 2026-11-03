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
	// ⚠️ 必须用官方形态的入参（32 位 hex + 当前时间）：op13 已收紧为
	//    "数字时间戳 + 32 位 hex"，用 pt-1 / 过期时间会被 400 拒掉（见下方校验用例）。
	tok := strings.Repeat("a1", 16)
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	body := challengeBody(tok, tsStr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/aichat/webhook", bytes.NewReader(body)))

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
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(tsStr+tok), sig) {
		t.Fatal("challenge signature invalid")
	}
}

// challengeBody 造 op13 请求体。
func challengeBody(token, ts string) []byte {
	b, _ := json.Marshal(map[string]any{
		"op": 13,
		"d":  map[string]string{"plain_token": token, "event_ts": ts},
	})
	return b
}

// serve 用给定 body/头跑一次请求。
func serve(h *Handler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/aichat/webhook", bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestChallengeInputValidation 覆盖 op13 的输入校验（B-02 验收项 + 2026-11 文档对齐）：
// 只有"字母数字 token + 数字时间戳 + 窗口内"才出签名。
//
// 口径来自官方文档示例：plain_token 是 20 位字母数字（"Arq0D5A61EgUu4OxUvOp"），
// 所以不能锁死成 32 位 hex（那会把平台自己的挑战全拒掉，回调地址永远验证不通过）。
func TestChallengeInputValidation(t *testing.T) {
	now := strconv.FormatInt(time.Now().Unix(), 10)
	stale := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	tok := strings.Repeat("a1", 16)

	cases := []struct {
		name  string
		token string
		ts    string
		want  int
	}{
		{"合法 32 位 hex + 当前时间", tok, now, http.StatusOK},
		{"大写 hex 放行", strings.Repeat("A1", 16), now, http.StatusOK},
		{"官方示例 20 位字母数字", "Arq0D5A61EgUu4OxUvOp", now, http.StatusOK},
		{"太短（旧用例的 pt-1）", "pt-1", now, http.StatusBadRequest},
		{"超长 65 位字母数字", strings.Repeat("ab", 32) + "c", now, http.StatusBadRequest},
		{"含非字母数字字符", "pt-1abcdefgh", now, http.StatusBadRequest},
		{"event_ts 非数字", tok, "abc", http.StatusBadRequest},
		{"event_ts 缺失", tok, "", http.StatusBadRequest},
		{"event_ts 超窗", tok, stale, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, _ := newHandler(t, Options{})
			rec := serve(h, challengeBody(c.token, c.ts), nil)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, c.want, rec.Body.String())
			}
			if c.want != http.StatusOK && strings.Contains(rec.Body.String(), "signature") {
				t.Fatalf("被拒的挑战不应返回签名: %s", rec.Body.String())
			}
		})
	}
}

// TestChallengeAppIDMismatch 校验 op13 的 appid 头与 op0 同口径。
func TestChallengeAppIDMismatch(t *testing.T) {
	h, _, _ := newHandler(t, Options{AppID: "app-1"})
	tok := strings.Repeat("a1", 16)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	rec := serve(h, challengeBody(tok, ts), map[string]string{AppIDHeader: "other"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("appid 不匹配 status = %d, want 401", rec.Code)
	}
	rec = serve(h, challengeBody(tok, ts), map[string]string{AppIDHeader: "app-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("appid 匹配 status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

// TestOp13ReplayAttackRejected 复现审计里的签名预言机攻击（B-02）：
// 攻击链是 ① op13 把"想伪造的事件体"当 plain_token 骗出签名 → ② 以同一 event_ts 作
// 时间戳、该 token 作 body 发 op0，验签必过。修复后第 ① 步就断了。
//
// 同时验证：即便拿一个【合法】挑战签名去重放 op0，也必须 401——
// 因为被签名的是 ts+32hex，而 op0 验的是 ts+body，body 只要不是那 32 位 hex 就不匹配。
func TestOp13ReplayAttackRejected(t *testing.T) {
	h, _, events := newHandler(t, Options{})
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	forged := []byte(`{"id":"EV-EVIL","op":0,"t":"GROUP_DEL_ROBOT","d":{"group_openid":"g1"}}`)

	// ① 把事件体当 plain_token 送进 op13：必须 400 且不产出签名。
	rec := serve(h, challengeBody(string(forged), ts), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("伪造 token 的挑战 status = %d, want 400（签名预言机未堵住）", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "signature") {
		t.Fatalf("被拒的挑战返回了签名: %s", rec.Body.String())
	}

	// ② 用【合法】挑战拿到的签名去重放 op0：必须 401 且不派发事件。
	tok := strings.Repeat("a1", 16)
	rec = serve(h, challengeBody(tok, ts), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法挑战 status = %d, want 200", rec.Code)
	}
	var resp struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad challenge resp: %v", err)
	}
	rec = serve(h, forged, map[string]string{
		SignatureHeader: resp.Signature,
		TimestampHeader: ts,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("挑战签名重放 status = %d, want 401", rec.Code)
	}
	select {
	case ev := <-events:
		t.Fatalf("伪造事件被派发: %+v", ev)
	case <-time.After(50 * time.Millisecond):
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
