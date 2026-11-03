package api

// validation_test.go 覆盖本地协议校验与重试头解析（避免无谓的 400 往返）。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/token"
)

// noServerClient 构造一个 BaseURL 不可达的客户端：用于断言"本地校验先于发请求"。
func noServerClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{AppID: "app", AppSecret: "secret", Tokens: token.NewMemory()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func TestPanelLocalValidation(t *testing.T) {
	c := noServerClient(t)
	cases := []struct {
		name string
		req  PanelCreateRequest
	}{
		{"scope 非法", PanelCreateRequest{Scope: "bad", Panel: Panel{}}},
		{"channel 不支持 specific", PanelCreateRequest{Scope: PanelScopeChannel, TargetType: PanelTargetSpecific}},
		{"c2c specific 缺 user_openids", PanelCreateRequest{Scope: PanelScopeC2C, TargetType: PanelTargetSpecific}},
		{"group specific 缺 group_openids", PanelCreateRequest{Scope: PanelScopeGroup, TargetType: PanelTargetSpecific}},
		{"元素超过 20", PanelCreateRequest{Scope: PanelScopeGroup, Panel: Panel{Items: make([]PanelItem, 21)}}},
		{"link 非 https", PanelCreateRequest{Scope: PanelScopeGroup, Panel: Panel{Items: []PanelItem{
			{Type: PanelItemLink, Name: "官网", Link: "http://example.com"}}}}},
		{"元素 name 超长", PanelCreateRequest{Scope: PanelScopeGroup, Panel: Panel{Items: []PanelItem{
			{Type: PanelItemCommand, Name: "123456789012345"}}}}},
		{"元素 type 非法", PanelCreateRequest{Scope: PanelScopeGroup, Panel: Panel{Items: []PanelItem{
			{Type: "bad", Name: "x"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.CreatePanel(context.Background(), tc.req); err == nil {
				t.Fatalf("CreatePanel(%s) error = nil, want 本地校验失败", tc.name)
			}
		})
	}
	// UpdatePanel 也走同一套内容校验。
	if _, err := c.UpdatePanel(context.Background(), "p1", Panel{Items: make([]PanelItem, 21)}); err == nil {
		t.Fatal("UpdatePanel(元素超限) error = nil")
	}
}

func TestMediaValidation(t *testing.T) {
	c := noServerClient(t)
	if _, err := c.UploadFileDetail(context.Background(), event.SceneGroup, "g1", "ftp://x/a.png", 1); err == nil {
		t.Fatal("非 http(s) URL 应该本地拒绝")
	}
	if _, err := c.UploadFileDetail(context.Background(), event.SceneGroup, "g1", "https://x/a.png", 9); err == nil {
		t.Fatal("file_type=9 应该本地拒绝")
	}
	if _, err := c.UploadFileData(context.Background(), event.SceneGroup, "g1", "a.png", 0, []byte("x")); err == nil {
		t.Fatal("file_type=0 应该本地拒绝")
	}
	// prepare 的三个校验和必填。
	base := UploadPrepareRequest{FileType: 1, FileSize: 10, FileName: "a.png",
		MD5: "0123456789abcdef0123456789abcdef", SHA1: "0123456789abcdef0123456789abcdef01234567", MD5TenM: "0123456789abcdef0123456789abcdef"}
	bad := base
	bad.MD5 = ""
	if _, err := c.UploadPrepare(context.Background(), event.SceneGroup, "g1", bad); err == nil {
		t.Fatal("缺 md5 应该本地拒绝")
	}
	bad = base
	bad.SHA1 = "zz"
	if _, err := c.UploadPrepare(context.Background(), event.SceneGroup, "g1", bad); err == nil {
		t.Fatal("非法 sha1 应该本地拒绝")
	}
	bad = base
	bad.FileType = 5
	if _, err := c.UploadPrepare(context.Background(), event.SceneGroup, "g1", bad); err == nil {
		t.Fatal("file_type=5 应该本地拒绝")
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	when := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(when)
	if d <= 0 {
		t.Fatalf("HTTP-date 形态未被解析: %q → %v", when, d)
	}
	if d > maxRetryAfter {
		t.Fatalf("HTTP-date 解析结果未受限: %v", d)
	}
	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Fatalf("秒数形态 = %v", got)
	}
	if got := parseRetryAfter("bad"); got != 0 {
		t.Fatalf("非法值 = %v, want 0", got)
	}
}
