package api

// apierr_shape_test.go 锁定平台错误体字段口径。
//
// 官方 v2《API 调用指南》写明错误体是 {"err_code":...,"message":...,"trace_id":...}，
// 并要求"依据 err_code 判断请求是否失败"。历史实现只读 "code"，导致所有错误
// 都退化成 Code=0、apierr 的判定方法（IsDuplicate/IsExpired/IsAuth/IsRateLimited）
// 在生产里静默失效。这里同时锁 err_code 优先与 code 兼容两条路径。

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kissqianfu/QQ-SDK/apierr"
	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

func TestAPIErrorReadsErrCode(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			writeJSON(w, 400, `{"err_code":40054005,"message":"消息被去重","trace_id":"trace-1"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1))
	if err == nil {
		t.Fatal("Send() error = nil, want APIError")
	}
	var apiErr *apierr.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T, want *apierr.APIError", err)
	}
	if apiErr.Code != 40054005 {
		t.Fatalf("Code = %d, want 40054005（err_code 未解析）", apiErr.Code)
	}
	if apiErr.TraceID != "trace-1" {
		t.Fatalf("TraceID = %q", apiErr.TraceID)
	}
	if !apiErr.IsDuplicate() {
		t.Fatal("IsDuplicate() = false, want true")
	}
}

func TestAPIErrorStillReadsLegacyCode(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			writeJSON(w, 403, `{"code":40034127,"message":"无 markdown 模板权限"}`)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	_, err := c.Send(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"},
		send.Markdown("hi").ReplyTo("msg-1", 1))
	var apiErr *apierr.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 40034127 || !apiErr.IsAuth() {
		t.Fatalf("err = %+v, want Code=40034127 IsAuth", err)
	}
}

func TestAPIErrorFallbackKeepsBody(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>bad gateway</html>"))
		}
	})
	c := newClient(t, srv, token.NewMemory())

	_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
		send.Text("hi").ReplyTo("msg-1", 1))
	var apiErr *apierr.APIError
	if !errors.As(err, &apiErr) || apiErr.HTTP != http.StatusBadGateway {
		t.Fatalf("err = %+v", err)
	}
	if apiErr.Code != 0 || apiErr.Message == "" {
		t.Fatalf("fallback = %+v, want 保留原始 body", apiErr)
	}
}
