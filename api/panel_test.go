package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestPanelLifecycle(t *testing.T) {
	var mu sync.Mutex
	var seen []struct {
		method, path string
		body         map[string]any
	}
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		seen = append(seen, struct {
			method, path string
			body         map[string]any
		}{r.Method, r.URL.Path, body})
		n := len(seen)
		mu.Unlock()

		switch {
		case n == 1: // POST /v2/panels 创建
			if r.Method != http.MethodPost || r.URL.Path != "/v2/panels" {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, `{"panel_id":"p_new"}`)
		case n == 2: // PUT /v2/panels/{id} 修改
			if r.Method != http.MethodPut || r.URL.Path != "/v2/panels/p_new" {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, `{"version":2}`)
		case n == 3: // GET 详情
			if r.Method != http.MethodGet || r.URL.Path != "/v2/panels/p_new" {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, `{"panel_id":"p_new","scope":"group","target_type":"specific","panel":{"items":[{"type":"command","name":"群签到","desc":"每日签到"}]},"version":2,"created_at":"2026-01-15T10:30:00Z","updated_at":"2026-01-16T10:30:00Z","group_openids":["G1"]}`)
		case n == 4: // PUT target
			if r.Method != http.MethodPut || r.URL.Path != "/v2/panels/p_new/target" {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, `{}`)
		case n == 5: // DELETE
			if r.Method != http.MethodDelete || r.URL.Path != "/v2/panels/p_new" {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, `{}`)
		}
	})
	c := newClient(t, srv, seededToken(t))
	ctx := context.Background()

	id, err := c.CreatePanel(ctx, PanelCreateRequest{
		Scope:        PanelScopeGroup,
		TargetType:   PanelTargetSpecific,
		GroupOpenIDs: []string{"G1", "G2"},
		Panel:        Panel{Items: []PanelItem{{Type: PanelItemCommand, Name: "群签到", Desc: "每日签到"}}, Remark: "签到面板"},
	})
	if err != nil || id != "p_new" {
		t.Fatalf("CreatePanel() = %q, %v", id, err)
	}

	ver, err := c.UpdatePanel(ctx, id, Panel{
		Items:  []PanelItem{{Type: PanelItemLink, Name: "更多服务", Link: "https://example.com", OnlyAdmin: true}},
		Remark: "更新备注",
	})
	if err != nil || ver != 2 {
		t.Fatalf("UpdatePanel() = %d, %v", ver, err)
	}

	detail, err := c.PanelDetail(ctx, id)
	if err != nil {
		t.Fatalf("PanelDetail() error = %v", err)
	}
	if detail.Scope != PanelScopeGroup || detail.TargetType != PanelTargetSpecific ||
		len(detail.Panel.Items) != 1 || len(detail.GroupOpenIDs) != 1 {
		t.Fatalf("detail = %+v", detail)
	}

	if err := c.UpdatePanelTargets(ctx, id, PanelTargetRequest{Op: PanelTargetOpAdd, GroupOpenIDs: []string{"G3"}}); err != nil {
		t.Fatalf("UpdatePanelTargets(add) error = %v", err)
	}
	if err := c.DeletePanel(ctx, id); err != nil {
		t.Fatalf("DeletePanel() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 5 {
		t.Fatalf("请求数 = %d", len(seen))
	}
	// 创建请求体逐字段校验。
	cre := seen[0].body
	if cre["scope"] != "group" || cre["target_type"] != "specific" {
		t.Fatalf("创建 body = %+v", cre)
	}
	groups, _ := cre["group_openids"].([]any)
	if len(groups) != 2 {
		t.Fatalf("创建 group_openids = %v", cre["group_openids"])
	}
	panel, _ := cre["panel"].(map[string]any)
	if panel == nil || panel["remark"] != "签到面板" {
		t.Fatalf("创建 panel = %v", cre["panel"])
	}
	// 修改请求体：面板内容在 panel 字段下。
	upd := seen[1].body
	p, _ := upd["panel"].(map[string]any)
	if p == nil || p["remark"] != "更新备注" {
		t.Fatalf("修改 body = %+v", upd)
	}
	// target 请求体。
	tg := seen[3].body
	if tg["op"] != "add" {
		t.Fatalf("target body = %+v", tg)
	}
}

func TestListPanelsPagination(t *testing.T) {
	var gotQuery string
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/panels" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		writeJSON(w, 200, `{"records":[{"panel_id":"p1","scope":"c2c","target_type":"all","panel":{"items":[{"type":"command","name":"查询天气","desc":"查询当前天气"}]},"version":1}],"next_cursor":"CUR","is_end":false}`)
	})
	c := newClient(t, srv, seededToken(t))

	list, err := c.ListPanels(context.Background(), PanelScopeC2C, "CUR", 10)
	if err != nil {
		t.Fatalf("ListPanels() error = %v", err)
	}
	if list.IsEnd || list.NextCursor != "CUR" || len(list.Records) != 1 || list.Records[0].PanelID != "p1" {
		t.Fatalf("list = %+v", list)
	}
	if q := gotQuery; !strings.Contains(q, "scope=c2c") || !strings.Contains(q, "cursor=CUR") || !strings.Contains(q, "limit=10") {
		t.Fatalf("query = %q", q)
	}
}

func TestListPanelsRequiresScope(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, _ *http.Request) {}), seededToken(t))
	if _, err := c.ListPanels(context.Background(), "", "", 0); err == nil {
		t.Fatal("ListPanels(scope=\"\") error = nil, want 校验错误")
	}
}

func TestPanelValidation(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, _ *http.Request) {}), seededToken(t))
	ctx := context.Background()

	if _, err := c.CreatePanel(ctx, PanelCreateRequest{}); err == nil {
		t.Fatal("CreatePanel(空 scope) error = nil, want 校验错误")
	}
	if err := c.DeletePanel(ctx, ""); err == nil {
		t.Fatal("DeletePanel(\"\") error = nil, want 校验错误")
	}
	if _, err := c.PanelDetail(ctx, ""); err == nil {
		t.Fatal("PanelDetail(\"\") error = nil, want 校验错误")
	}
	if _, err := c.UpdatePanel(ctx, "", Panel{}); err == nil {
		t.Fatal("UpdatePanel(\"\") error = nil, want 校验错误")
	}
	if err := c.UpdatePanelTargets(ctx, "p1", PanelTargetRequest{Op: "xor"}); err == nil {
		t.Fatal("UpdatePanelTargets(op=xor) error = nil, want 校验错误")
	}
}
