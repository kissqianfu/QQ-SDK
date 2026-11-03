package api

// menu_test.go 覆盖全局自定义菜单（2026-08-12 新增）与分享链接。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kissqianfu/QQ-SDK/token"
)

func TestMenuGetPut(t *testing.T) {
	var gotMethod string
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/menu":
			gotMethod = r.Method
			if r.Method == http.MethodGet {
				writeJSON(w, 200, `{"menu":{"items":[{"name":"帮助","type":"send_message","send_message":"/help"}]},"version":3}`)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"version":4}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	state, err := c.GetMenu(context.Background())
	if err != nil || state.Version != 3 || len(state.Menu.Items) != 1 {
		t.Fatalf("GetMenu() = %+v, %v", state, err)
	}
	if state.Menu.Items[0].SendMessage != "/help" {
		t.Fatalf("menu item = %+v", state.Menu.Items[0])
	}

	version, err := c.PutMenu(context.Background(), Menu{Items: []MenuItem{
		{Name: "帮助", Type: MenuItemSendMessage, SendMessage: "/help"},
		{Name: "更多", Type: MenuItemMenu, SubMenuItems: []MenuSubItem{
			{Name: "设置", Type: MenuItemSendMessage, SendMessage: "/settings"},
		}},
		{Name: "官网", Type: MenuItemLink, Link: "https://example.com"},
		{Name: "联网", Type: MenuItemSwitch, Switch: &MenuSwitch{SwitchID: "web", Default: true}},
	}})
	if err != nil || version != 4 {
		t.Fatalf("PutMenu() = %d, %v", version, err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s", gotMethod)
	}
	menu := gotBody["menu"].(map[string]any)
	if items := menu["items"].([]any); len(items) != 4 {
		t.Fatalf("items = %+v", items)
	}
}

func TestMenuValidation(t *testing.T) {
	c, err := New(Config{AppID: "a", AppSecret: "s", Tokens: token.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		menu Menu
	}{
		{"超过 10 个菜单项", Menu{Items: make([]MenuItem, 11)}},
		{"name 超宽", Menu{Items: []MenuItem{{Name: "一二三四五六", Type: MenuItemSendMessage, SendMessage: "x"}}}},
		{"link 非 https", Menu{Items: []MenuItem{{Name: "官网", Type: MenuItemLink, Link: "http://example.com"}}}},
		{"switch 缺 id", Menu{Items: []MenuItem{{Name: "开关", Type: MenuItemSwitch}}}},
		{"子菜单超 5 个", Menu{Items: []MenuItem{{Name: "更多", Type: MenuItemMenu,
			SubMenuItems: []MenuSubItem{{Name: "a", Type: MenuItemSendMessage, SendMessage: "a"}, {Name: "b", Type: MenuItemSendMessage, SendMessage: "b"}, {Name: "c", Type: MenuItemSendMessage, SendMessage: "c"}, {Name: "d", Type: MenuItemSendMessage, SendMessage: "d"}, {Name: "e", Type: MenuItemSendMessage, SendMessage: "e"}, {Name: "f", Type: MenuItemSendMessage, SendMessage: "f"}}}}}},
		{"子菜单不能嵌套 menu", Menu{Items: []MenuItem{{Name: "更多", Type: MenuItemMenu,
			SubMenuItems: []MenuSubItem{{Name: "x", Type: MenuItemMenu}}}}}},
		{"未知 type", Menu{Items: []MenuItem{{Name: "x", Type: "unknown"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.PutMenu(context.Background(), tc.menu); err == nil {
				t.Fatalf("PutMenu(%s) error = nil, want error", tc.name)
			}
		})
	}
}

func TestGenerateURLLink(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/generate_url_link":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"data":{"url":"https://q.qq.com/bot/add?x=1"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	link, err := c.GenerateURLLink(context.Background(), "campaign-1")
	if err != nil || link != "https://q.qq.com/bot/add?x=1" {
		t.Fatalf("GenerateURLLink() = %q, %v", link, err)
	}
	if gotBody["callback_data"] != "campaign-1" {
		t.Fatalf("body = %+v", gotBody)
	}
	// 超过 32 字符本地拒绝。
	if _, err := c.GenerateURLLink(context.Background(), strings.Repeat("x", 33)); err == nil {
		t.Fatal("callback_data 超长应该报错")
	}
}
