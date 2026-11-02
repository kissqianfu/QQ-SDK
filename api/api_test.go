package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/apierr"
	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

// fakeStore 记录 Set 的 ttl，用于验证"提前 90s 过期"。
type fakeStore struct {
	mu   sync.Mutex
	tok  string
	ttl  time.Duration
	gets int
}

func (f *fakeStore) Get(context.Context) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	return f.tok, f.tok != "", nil
}

func (f *fakeStore) Set(_ context.Context, tok string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tok, f.ttl = tok, ttl
	return nil
}

// testServer 启动一个同时处理 token 与 API 的测试服务。
func testServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

// newClient 用测试服务构造客户端（no-op sleep 让重试即时）。
func newClient(t *testing.T, srv *httptest.Server, store token.Store) *Client {
	t.Helper()
	c, err := New(Config{
		AppID:     "app",
		AppSecret: "secret",
		BaseURL:   srv.URL,
		TokenURL:  srv.URL + "/app/getAppAccessToken",
		Tokens:    store,
		Sleep:     func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func TestTokenFetchesAndCaches(t *testing.T) {
	var tokenCalls int
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		tokenCalls++
		writeJSON(w, 200, `{"access_token":"tok-1","expires_in":7200}`)
	})

	store := &fakeStore{}
	c := newClient(t, srv, store)

	for i := 0; i < 3; i++ {
		tok, err := c.Token(context.Background())
		if err != nil || tok != "tok-1" {
			t.Fatalf("Token() = %q/%v", tok, err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (缓存)", tokenCalls)
	}
	if want := 7200*time.Second - refreshAhead; store.ttl != want {
		t.Fatalf("stored ttl = %v, want %v", store.ttl, want)
	}
}

func TestTokenEmptyIsError(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"access_token":"","expires_in":7200}`)
	})
	c := newClient(t, srv, token.NewMemory())
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("Token() error = nil, want error")
	}
}

// 官方文档：token 接口业务失败时 HTTP 仍是 200，错误在响应体 code/message。
func TestTokenBusinessErrorHTTP200(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"code":100014,"message":"invalid appid"}`)
	})
	c := newClient(t, srv, token.NewMemory())
	_, err := c.Token(context.Background())
	if err == nil {
		t.Fatal("Token() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "code=100014") || !strings.Contains(err.Error(), "invalid appid") {
		t.Fatalf("err = %v, want 平台 code/message 透传", err)
	}
}

func TestSendGroupBody(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
			return
		case "/v2/groups/g1/messages":
			if auth := r.Header.Get("Authorization"); auth != "QQBot tok" {
				t.Errorf("Authorization = %q", auth)
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m1","timestamp":"1700000000000"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	res, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("你好").ReplyTo("msg-1", 2))
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if res.ID != "m1" || res.Timestamp.UnixMilli() != 1700000000000 {
		t.Fatalf("result = %+v", res)
	}
	if gotBody["msg_type"].(float64) != 0 || gotBody["content"] != "你好" {
		t.Fatalf("body = %+v", gotBody)
	}
	if gotBody["msg_id"] != "msg-1" || gotBody["msg_seq"].(float64) != 2 {
		t.Fatalf("reply anchor missing: %+v", gotBody)
	}
}

func TestSendMarkdownWithKeyboardAndReference(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/users/u1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m2","timestamp":1700000000001}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	kb := send.Keyboard{Rows: [][]send.Button{{{ID: "b1", Label: "去", ActionType: 0, Link: "https://example.com"}}}}
	_, err := c.Send(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"},
		send.Markdown("# 标题").Keyboard(kb).Reference("REFIDX_1"))
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotBody["msg_type"].(float64) != 2 {
		t.Fatalf("msg_type = %v, want 2", gotBody["msg_type"])
	}
	md := gotBody["markdown"].(map[string]any)
	if md["content"] != "# 标题" {
		t.Fatalf("markdown = %+v", md)
	}
	if gotBody["message_reference"] == nil || gotBody["keyboard"] == nil {
		t.Fatalf("reference/keyboard missing: %+v", gotBody)
	}
}

func TestSendRejectsUnknownSceneAndInvalidMessage(t *testing.T) {
	calls := 0
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
	})
	c := newClient(t, srv, token.NewMemory())

	if _, err := c.Send(context.Background(), Target{Scene: 99, ID: "x"}, send.Text("hi")); err == nil {
		t.Fatal("Send(unknown scene) error = nil, want error")
	}
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g"}, send.Text("  ")); err == nil {
		t.Fatal("Send(invalid message) error = nil, want error")
	}
	if calls != 0 {
		t.Fatalf("server called %d times for invalid requests", calls)
	}
}

func TestMe(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/users/@me":
			writeJSON(w, 200, `{"id":"bot-1","username":"小助手","avatar":"a","bot":true}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	user, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if user.ID != "bot-1" || user.Username != "小助手" || !user.Bot {
		t.Fatalf("Me() = %+v", user)
	}
}

func TestInteract(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			b, _ := io.ReadAll(r.Body)
			gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(b)
			writeJSON(w, 200, `{}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	if err := c.Interact(context.Background(), "it-1", InteractOK); err != nil {
		t.Fatalf("Interact() error = %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/interactions/it-1" || gotBody != `{"code":0}` {
		t.Fatalf("interact = %s %s %s", gotMethod, gotPath, gotBody)
	}
	if err := c.Interact(context.Background(), "", InteractOK); err == nil {
		t.Fatal("Interact(empty id) error = nil, want error")
	}
}

func TestSendMarkdownTemplateWithKeyboardTemplateAndEventID(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m3"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	msg := send.NewMarkdownTemplate("65",
		send.MarkdownParam{Key: "title", Values: []string{"标题"}},
		send.MarkdownParam{Key: "content", Values: []string{"内容1", "内容2"}},
	).KeyboardTemplate("kb-tpl-1").ReplyToEvent("EV-77", 1)
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotBody["msg_type"].(float64) != 2 {
		t.Fatalf("msg_type = %v", gotBody["msg_type"])
	}
	md := gotBody["markdown"].(map[string]any)
	if md["custom_template_id"] != "65" {
		t.Fatalf("markdown = %+v (custom_template_id 应为 string)", md)
	}
	params := md["params"].([]any)
	first := params[0].(map[string]any)
	if first["key"] != "title" || first["values"].([]any)[0] != "标题" {
		t.Fatalf("params[0] = %+v", first)
	}
	kb := gotBody["keyboard"].(map[string]any)
	if kb["id"] != "kb-tpl-1" || kb["content"] != nil {
		t.Fatalf("keyboard = %+v (短形式只传 id)", kb)
	}
	if gotBody["event_id"] != "EV-77" || gotBody["msg_seq"].(float64) != 1 || gotBody["msg_id"] != nil {
		t.Fatalf("event anchor = %+v", gotBody)
	}
}

func TestRetryOn5xx(t *testing.T) {
	attempts := 0
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/users/@me":
			attempts++
			if attempts == 1 {
				writeJSON(w, 500, `{"code":500,"message":"boom"}`)
				return
			}
			writeJSON(w, 200, `{"id":"bot","username":"b","bot":true}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me() after retry error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (5xx 重试)", attempts)
	}
}

func Test429HonorsRetryAfter(t *testing.T) {
	attempts := 0
	var sleeps []time.Duration
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/users/@me":
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "2")
				writeJSON(w, 429, `{"code":40034100,"message":"rate limited"}`)
				return
			}
			writeJSON(w, 200, `{"id":"bot","username":"b","bot":true}`)
		}
	})
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(d time.Duration) { sleeps = append(sleeps, d) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (429 重试)", attempts)
	}
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("sleeps = %v, want [2s]（Retry-After）", sleeps)
	}
}

func TestCancelStopsRetry(t *testing.T) {
	attempts := 0
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/users/@me":
			attempts++
			writeJSON(w, 500, `{"code":500,"message":"boom"}`)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := New(Config{
		AppID: "app", AppSecret: "secret",
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/app/getAppAccessToken",
		Tokens:   token.NewMemory(),
		Sleep:    func(time.Duration) { cancel() }, // 第一次等待时取消 ctx
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.Me(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Me() error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1（取消后不再重试）", attempts)
	}
}

func TestDuplicateErrorIsNotRetried(t *testing.T) {
	attempts := 0
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/messages":
			attempts++
			writeJSON(w, 400, `{"code":40054005,"message":"dup","trace_id":"t1"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, send.Text("hi"))
	if err == nil {
		t.Fatal("Send() error = nil, want APIError")
	}
	apiErr, ok := err.(*apierr.APIError)
	if !ok || !apiErr.IsDuplicate() || apiErr.TraceID != "t1" {
		t.Fatalf("err = %#v, want duplicate APIError", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (4xx 不重试)", attempts)
	}
}

func TestParseSendResult(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantID    string
		wantMilli int64
		wantSec   int64
		wantRFC   string
		wantRef   string
		wantZero  bool
	}{
		{name: "string timestamp", raw: `{"id":"x","timestamp":"1700000000000"}`, wantID: "x", wantMilli: 1700000000000},
		{name: "number timestamp", raw: `{"id":"x","timestamp":1700000000000}`, wantID: "x", wantMilli: 1700000000000},
		{name: "number seconds", raw: `{"id":"x","timestamp":1793400000}`, wantID: "x", wantSec: 1793400000},
		{name: "string seconds", raw: `{"id":"x","timestamp":"1793400000"}`, wantID: "x", wantSec: 1793400000},
		{name: "rfc3339 timestamp", raw: `{"id":"x","timestamp":"2026-07-21T10:00:00+08:00"}`, wantID: "x", wantRFC: "2026-07-21T10:00:00+08:00"},
		{name: "ref_idx", raw: `{"id":"x","ext_info":{"ref_idx":"REFIDX_1=="}}`, wantID: "x", wantZero: true, wantRef: "REFIDX_1=="},
		{name: "no timestamp", raw: `{"id":"x"}`, wantID: "x", wantZero: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSendResult([]byte(tt.raw))
			if err != nil {
				t.Fatalf("parseSendResult() error = %v", err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if got.RefIdx != tt.wantRef {
				t.Fatalf("RefIdx = %q, want %q", got.RefIdx, tt.wantRef)
			}
			if tt.wantZero {
				if !got.Timestamp.IsZero() {
					t.Fatalf("Timestamp = %v, want zero", got.Timestamp)
				}
				return
			}
			if tt.wantRFC != "" {
				if got.Timestamp.Format(time.RFC3339) != tt.wantRFC {
					t.Fatalf("Timestamp = %v, want %v", got.Timestamp, tt.wantRFC)
				}
				return
			}
			if tt.wantSec != 0 {
				if got.Timestamp.Unix() != tt.wantSec {
					t.Fatalf("Timestamp = %v, want unix sec %d", got.Timestamp, tt.wantSec)
				}
				return
			}
			if got.Timestamp.UnixMilli() != tt.wantMilli {
				t.Fatalf("Timestamp = %v, want milli %d", got.Timestamp, tt.wantMilli)
			}
		})
	}
}

func TestSendCardBody(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m9"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	card := send.Card{Title: "标题", Description: "描述", URL: "https://e.com/a", PicURL: "https://e.com/a.png"}
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, send.NewCard(card)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotBody["msg_type"].(float64) != 8 {
		t.Fatalf("msg_type = %v, want 8", gotBody["msg_type"])
	}
	cardBody := gotBody["card"].(map[string]any)
	if cardBody["type"] != "tuwen" {
		t.Fatalf("card = %+v", cardBody)
	}
	content := cardBody["content"].(map[string]any)
	if content["title"] != "标题" || content["url"] != "https://e.com/a" || content["description"] != "描述" {
		t.Fatalf("card content = %+v", content)
	}
	if gotBody["content"] != nil {
		t.Fatalf("msg_type=8 时不应有 content 字段: %+v", gotBody)
	}
}

func TestSendMediaBody(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m10"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, send.Media("file-1")); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotBody["msg_type"].(float64) != 7 {
		t.Fatalf("msg_type = %v, want 7", gotBody["msg_type"])
	}
	if gotBody["content"] != " " {
		t.Fatalf("msg_type=7 时 content 必须为空格: %+v", gotBody)
	}
	if media := gotBody["media"].(map[string]any); media["file_info"] != "file-1" {
		t.Fatalf("media = %+v", media)
	}
}

func TestSendActiveMsgSeq(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m11"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, send.Markdown("hi").Seq(7)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotBody["msg_seq"].(float64) != 7 {
		t.Fatalf("主动消息 msg_seq = %v, want 7: %+v", gotBody["msg_seq"], gotBody)
	}
	if gotBody["msg_id"] != nil || gotBody["event_id"] != nil {
		t.Fatalf("主动消息不应带锚点: %+v", gotBody)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New(empty) error = nil, want error")
	}
}

func TestTokenStringExpiresIn(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"access_token":"tok-2","expires_in":"7200"}`)
	})
	store := &fakeStore{}
	c := newClient(t, srv, store)
	tok, err := c.Token(context.Background())
	if err != nil || tok != "tok-2" {
		t.Fatalf("Token() = %q/%v", tok, err)
	}
	if want := 7200*time.Second - refreshAhead; store.ttl != want {
		t.Fatalf("ttl = %v, want %v", store.ttl, want)
	}
}

func TestUploadFileDetail(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g-1/files":
			writeJSON(w, 200, `{"file_uuid":"uuid-1","file_info":"fi-1","ttl":300,"raw_url":"https://cos/x"}`)
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	res, err := c.UploadFileDetail(context.Background(), event.SceneGroup, "g-1", "https://example.com/a.png", 1)
	if err != nil {
		t.Fatalf("UploadFileDetail() error = %v", err)
	}
	if res.FileInfo != "fi-1" || res.FileUUID != "uuid-1" || res.TTL != 300 || res.RawURL != "https://cos/x" {
		t.Fatalf("UploadFileDetail() = %+v", res)
	}
	// 字符串便捷方法仍只返回 file_info。
	fi, err := c.UploadFile(context.Background(), event.SceneGroup, "g-1", "https://example.com/a.png", 1)
	if err != nil || fi != "fi-1" {
		t.Fatalf("UploadFile() = %q/%v", fi, err)
	}
}

func TestUploadFileDataBase64(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/users/u-1/files":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			writeJSON(w, 200, `{"file_info":"fi-b64","ttl":0}`)
		default:
			http.NotFound(w, r)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	fi, err := c.UploadFileData(context.Background(), event.SceneC2C, "u-1", "sticker.png", 1, []byte("\x89PNG-data"))
	if err != nil || fi != "fi-b64" {
		t.Fatalf("UploadFileData() = %q/%v", fi, err)
	}
	if gotBody["file_type"].(float64) != 1 || gotBody["file_name"] != "sticker.png" || gotBody["srv_send_msg"] != false {
		t.Fatalf("body = %+v", gotBody)
	}
	if decoded, err := base64.StdEncoding.DecodeString(gotBody["file_data"].(string)); err != nil || string(decoded) != "\x89PNG-data" {
		t.Fatalf("file_data base64 解码不符: %v", err)
	}
	// 超 5M 拒绝。
	if _, err := c.UploadFileData(context.Background(), event.SceneC2C, "u-1", "big.bin", 4, make([]byte, maxFileDataBytes+1)); err == nil {
		t.Fatal("UploadFileData(>5M) error = nil, want error")
	}
}

func TestDefaultBaseURL(t *testing.T) {
	if ProdBaseURL != "https://api.bot.qq.com" {
		t.Fatalf("ProdBaseURL = %q, want https://api.bot.qq.com", ProdBaseURL)
	}
}
