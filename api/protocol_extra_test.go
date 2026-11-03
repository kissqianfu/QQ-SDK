package api

// protocol_extra_test.go 覆盖 2026-10 文档新增/修正的协议字段：
// is_wakeup、force_verify_image_resource、action.anchor、permission.specify_role_ids、
// 撤回消息（DELETE）、输入中状态（msg_type=6）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

func TestSendIsWakeupAndForceVerifyImage(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/users/u1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m1","timestamp":"2026-07-21T10:30:00+08:00"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	msg := send.Markdown("![img](https://example.com/a.png)").ForceVerifyImageResource(true)
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"}, msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	md, ok := gotBody["markdown"].(map[string]any)
	if !ok {
		t.Fatalf("markdown 字段类型错误: %+v", gotBody["markdown"])
	}
	if md["force_verify_image_resource"] != true {
		t.Fatalf("force_verify_image_resource = %v, want true", md["force_verify_image_resource"])
	}
	if _, sent := gotBody["is_wakeup"]; sent {
		t.Fatalf("is_wakeup 不该出现: %+v", gotBody)
	}

	// 互动召回：is_wakeup=true 且必须没有 msg_id/event_id 锚点。
	gotBody = nil
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"},
		send.Text("好久不见").IsWakeup(true).Seq(3)); err != nil {
		t.Fatalf("Send(wakeup) error = %v", err)
	}
	if gotBody["is_wakeup"] != true {
		t.Fatalf("is_wakeup = %v, want true", gotBody["is_wakeup"])
	}
	if _, ok := gotBody["msg_id"]; ok {
		t.Fatalf("wakeup 不应带 msg_id: %+v", gotBody)
	}
}

func TestSendKeyboardAnchorAndRoleIDs(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"m1","timestamp":"2026-07-21T10:30:00+08:00"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	kb := send.Keyboard{Rows: [][]send.Button{{
		{ID: "b1", Label: "选图", ActionType: 2, ActionData: "/选图", Anchor: 1},
		{ID: "b2", Label: "管理员", ActionType: 1, ActionData: "x", SpecifyRoleIDs: []string{"r1"}},
	}}}
	if _, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Markdown("hi").Keyboard(kb).ReplyTo("msg-1", 1)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	rows, ok := gotBody["keyboard"].(map[string]any)["content"].(map[string]any)["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("keyboard rows = %+v", gotBody["keyboard"])
	}
	buttons := rows[0].(map[string]any)["buttons"].([]any)
	b1 := buttons[0].(map[string]any)["action"].(map[string]any)
	if b1["anchor"] != float64(1) {
		t.Fatalf("anchor = %v, want 1", b1["anchor"])
	}
	b2 := buttons[1].(map[string]any)
	perm := b2["action"].(map[string]any)["permission"].(map[string]any)
	if perm["type"] != float64(0) {
		t.Fatalf("permission.type = %v, want 0（指定对象）", perm["type"])
	}
	roles, ok := perm["specify_role_ids"].([]any)
	if !ok || len(roles) != 1 || roles[0] != "r1" {
		t.Fatalf("specify_role_ids = %v", perm["specify_role_ids"])
	}
}

func TestRecallMessage(t *testing.T) {
	var gotMethod, gotPath string
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			gotMethod, gotPath = r.Method, r.URL.Path
			writeJSON(w, 200, `{}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	if err := c.Recall(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, "m1"); err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/groups/g1/messages/m1" {
		t.Fatalf("recall request = %s %s", gotMethod, gotPath)
	}

	// 空 message_id 必须在本地就拒绝，不发请求。
	if err := c.Recall(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, ""); err == nil {
		t.Fatal("Recall(empty id) error = nil, want error")
	}
}

func TestInputNotify(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			gotPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			writeJSON(w, 200, `{}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	if err := c.InputNotify(context.Background(), "u1", "msg-1", 2, 30); err != nil {
		t.Fatalf("InputNotify() error = %v", err)
	}
	if gotPath != "/v2/users/u1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["msg_type"] != float64(6) {
		t.Fatalf("msg_type = %v, want 6", gotBody["msg_type"])
	}
	in, ok := gotBody["input_notify"].(map[string]any)
	if !ok || in["input_type"] != float64(1) || in["input_second"] != float64(30) {
		t.Fatalf("input_notify = %+v", gotBody["input_notify"])
	}
	if gotBody["msg_id"] != "msg-1" || gotBody["msg_seq"] != float64(2) {
		t.Fatalf("anchor = %+v", gotBody)
	}

	// 缺 msg_id：本地拒绝（平台会当成主动消息/直接报错）。
	if err := c.InputNotify(context.Background(), "u1", "", 1, 0); err == nil {
		t.Fatal("InputNotify(empty msg_id) error = nil, want error")
	}
	// 超过 60 秒：本地拒绝。
	if err := c.InputNotify(context.Background(), "u1", "msg-1", 1, 61); err == nil {
		t.Fatal("InputNotify(61s) error = nil, want error")
	}
}

func TestStartStreamDefaultsContentType(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/users/u1/stream_messages":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"id":"stream-1","timestamp":"2026-07-21T10:30:00+08:00"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	if _, err := c.StartStream(context.Background(),
		Target{Scene: event.SceneC2C, ID: "u1"}, "正在生成", StreamConfig{MsgID: "msg-1"}); err != nil {
		t.Fatalf("StartStream() error = %v", err)
	}
	if gotBody["content_type"] != StreamText {
		t.Fatalf("content_type = %v, want %q（空值必须归一化，不能原样下发）",
			gotBody["content_type"], StreamText)
	}
}
