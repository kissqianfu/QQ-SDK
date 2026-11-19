package api

import (
	"strings"
	"testing"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

// TestStaticPaths 锁定静态路径：路径是平台契约，改错等于整类接口 404。
func TestStaticPaths(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"groupsPath", groupsPath, "/v2/groups"},
		{"usersPath", usersPath, "/v2/users"},
		{"menuPath", menuPath, "/v2/menu"},
		{"panelsPath", panelsPath, "/v2/panels"},
		{"mediaPath", mediaPath, "/v2/media-images"},
		{"urlLinkPath", urlLinkPath, "/v2/generate_url_link"},
		{"strategyBasePath", strategyBasePath, "/v2/groups/join_approval_strategy"},
		{"mePath（官方不带 /v2）", mePath, "/users/@me"},
		{"interactionsPath（官方不带 /v2）", interactionsPath, "/interactions"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q，期望 %q", c.name, c.got, c.want)
		}
	}
}

// TestPathBuilders 锁定带动态段的拼装结果。
func TestPathBuilders(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"groupPath", groupPath("g1", "/info"), "/v2/groups/g1/info"},
		{"groupPath 空后缀", groupPath("g1", ""), "/v2/groups/g1"},
		{"userPath", userPath("u1", "/info"), "/v2/users/u1/info"},
		{"userPath 空后缀", userPath("u1", ""), "/v2/users/u1"},
		{"panelPath", panelPath("p1", ""), "/v2/panels/p1"},
		{"panelPath target", panelPath("p1", "/target"), "/v2/panels/p1/target"},
		{"strategyPath", strategyPath("s1", "/execute"), "/v2/groups/join_approval_strategy/s1/execute"},
		{"materialPath", materialPath("img1", "/delete"), "/v2/media-images/img1/delete"},
		{"interactionPath", interactionPath("i1"), "/interactions/i1"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q，期望 %q", c.name, c.got, c.want)
		}
	}
}

// TestItemPathEscapes 保证 ID 里的分隔符与空格不会把路径切成多段（转义只在 itemPath 一处做）。
func TestItemPathEscapes(t *testing.T) {
	got := groupPath("a/b c?d", "/info")
	if want := "/v2/groups/a%2Fb%20c%3Fd/info"; got != want {
		t.Errorf("groupPath = %q，期望 %q", got, want)
	}
	if strings.Count(got, "/") != 4 {
		t.Errorf("被转义的 ID 不该新增路径分隔符：%q", got)
	}
}

// TestScenePath 覆盖两种场景与两条拒绝分支。
func TestScenePath(t *testing.T) {
	if p, err := scenePath(event.SceneGroup, "g1", "/messages"); err != nil || p != "/v2/groups/g1/messages" {
		t.Errorf("群聊场景 = %q, %v", p, err)
	}
	if p, err := scenePath(event.SceneC2C, "u1", "/messages"); err != nil || p != "/v2/users/u1/messages" {
		t.Errorf("单聊场景 = %q, %v", p, err)
	}
	if _, err := scenePath(event.SceneGroup, "", "/messages"); err == nil {
		t.Error("空目标 id 应报错")
	}
	if _, err := scenePath(event.Scene(99), "x", ""); err == nil {
		t.Error("未知场景应报错")
	}
}

// TestMessagePath 确认发消息路径走场景前缀。
func TestMessagePath(t *testing.T) {
	p, err := messagePath(Target{Scene: event.SceneC2C, ID: "u1"})
	if err != nil || p != "/v2/users/u1/messages" {
		t.Errorf("messagePath = %q, %v", p, err)
	}
}
