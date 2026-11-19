package api

// user_test.go 覆盖用户基本信息接口（2026-11-16 新增 GET /v2/users/{user_openid}/info）。

import (
	"context"
	"net/http"
	"testing"
)

func TestUserInfo(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/users/U1/info" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, `{"username":"笑😊心❤️","allow_proactive_msg":true,
			"avatar":"https://thirdqq.qlogo.cn/bqqthird?key=example&s=640","union_openid":"uo-1"}`)
	})
	c := newClient(t, srv, seededToken(t))

	info, err := c.UserInfo(context.Background(), "U1")
	if err != nil {
		t.Fatalf("UserInfo() error = %v", err)
	}
	if info.Username != "笑😊心❤️" || !info.AllowProactiveMsg || info.UnionOpenID != "uo-1" {
		t.Fatalf("info = %+v", info)
	}
	if info.Avatar == "" {
		t.Fatal("Avatar 为空")
	}
}

func TestUserInfoEmptyOpenID(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, _ *http.Request) {}), seededToken(t))
	if _, err := c.UserInfo(context.Background(), ""); err == nil {
		t.Fatal("空 user_openid 应报错")
	}
}
