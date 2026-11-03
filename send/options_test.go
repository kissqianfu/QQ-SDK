package send

// options_test.go 覆盖 2026-10 文档新增的出站字段：is_wakeup、force_verify_image_resource、
// 按钮 anchor / permission.type / specify_role_ids，以及 @全体成员 常量。

import "testing"

func TestWakeupMutuallyExclusiveWithAnchors(t *testing.T) {
	if err := Text("hi").IsWakeup(true).Validate(); err != nil {
		t.Fatalf("IsWakeup(true) Validate() error = %v", err)
	}
	if err := Text("hi").IsWakeup(true).ReplyTo("m1", 1).Validate(); err == nil {
		t.Fatal("is_wakeup + msg_id 应该被拒绝")
	}
	if err := Text("hi").IsWakeup(true).ReplyToEvent("e1", 1).Validate(); err == nil {
		t.Fatal("is_wakeup + event_id 应该被拒绝")
	}
	if m := Text("hi").IsWakeup(true); !m.WakeupEnabled() {
		t.Fatal("WakeupEnabled() = false")
	}
}

func TestForceVerifyImageOnlyForMarkdown(t *testing.T) {
	if err := Markdown("hi").ForceVerifyImageResource(true).Validate(); err != nil {
		t.Fatalf("Markdown + force verify Validate() error = %v", err)
	}
	if !Markdown("hi").ForceVerifyImageResource(true).ImageVerifyEnabled() {
		t.Fatal("ImageVerifyEnabled() = false")
	}
	if err := Text("hi").ForceVerifyImageResource(true).Validate(); err == nil {
		t.Fatal("force_verify_image_resource 只该对 Markdown 生效")
	}
}

func TestButtonAnchorAndPermissionTypeValidate(t *testing.T) {
	kb := Keyboard{Rows: [][]Button{{
		{ID: "b1", Label: "选图", ActionType: 2, ActionData: "/选图", Anchor: 1},
		{ID: "b2", Label: "管理", ActionType: 1, ActionData: "x", PermissionType: 1, SpecifyRoleIDs: []string{"r1"}},
	}}}
	if err := Markdown("hi").Keyboard(kb).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if AtEveryone != "<qqbot-at-everyone />" {
		t.Fatalf("AtEveryone = %q", AtEveryone)
	}
}
