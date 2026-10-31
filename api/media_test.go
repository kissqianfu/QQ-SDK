package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/token"
)

func TestUploadFile(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		default:
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"file_info":"fi-1","ttl":3600}`)
		}
	})
	c := newClient(t, srv, nil)

	info, err := c.UploadFile(context.Background(), event.SceneGroup, "g1", "https://example.com/a.png", 1)
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if info != "fi-1" || gotPath != "/v2/groups/g1/files" || gotAuth != "QQBot tok" {
		t.Fatalf("info/path/auth = %q/%q/%q", info, gotPath, gotAuth)
	}
	if gotBody["file_type"].(float64) != 1 || gotBody["url"] != "https://example.com/a.png" {
		t.Fatalf("body = %+v", gotBody)
	}
}

func TestUploadFileErrors(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
			return
		}
		writeJSON(w, 200, `{}`)
	})
	c := newClient(t, srv, nil)
	if _, err := c.UploadFile(context.Background(), event.SceneC2C, "u1", "https://example.com/a.png", 1); err == nil {
		t.Fatal("UploadFile(no file_info) error = nil")
	}
	if _, err := c.UploadFile(context.Background(), 99, "x", "https://example.com/a.png", 1); err == nil {
		t.Fatal("UploadFile(unknown scene) error = nil")
	}
	if _, err := c.UploadFile(context.Background(), event.SceneGroup, "", "https://example.com/a.png", 1); err == nil {
		t.Fatal("UploadFile(empty target) error = nil")
	}
}

// TestUploadFileBytes 验证二进制分片上传完整链路：
// prepare（带 md5/sha1/md5_10m）→ 原始字节 PUT → part_finish → 带 upload_id 合并。
func TestUploadFileBytes(t *testing.T) {
	data := []byte("abcdef") // 6 字节，block_size=2 → 3 片
	var prepareBody, mergeBody map[string]any
	var puts [][]byte
	var finishes []map[string]any
	var srv *httptest.Server
	srv = testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case r.URL.Path == "/v2/groups/g1/upload_prepare":
			if auth := r.Header.Get("Authorization"); auth != "QQBot tok" {
				t.Errorf("prepare Authorization = %q", auth)
			}
			_ = json.NewDecoder(r.Body).Decode(&prepareBody)
			writeJSON(w, 200, `{"upload_id":"up-1","block_size":"2","parts":[
				{"index":0,"presigned_url":"`+srv.URL+`/put/0","block_size":"2"},
				{"index":1,"presigned_url":"`+srv.URL+`/put/1","block_size":"2"},
				{"index":2,"presigned_url":"`+srv.URL+`/put/2","block_size":"2"}],
				"upload_config":{"concurrency":1}}`)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/put/"):
			if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("PUT Content-Type = %q", ct)
			}
			body, _ := io.ReadAll(r.Body)
			puts = append(puts, body)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v2/groups/g1/upload_part_finish":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			finishes = append(finishes, body)
			writeJSON(w, 200, `{}`)
		case r.URL.Path == "/v2/groups/g1/files":
			_ = json.NewDecoder(r.Body).Decode(&mergeBody)
			writeJSON(w, 200, `{"file_info":"fi-merge","ttl":300}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	info, err := c.UploadFileBytes(context.Background(), event.SceneGroup, "g1", "a.png", 1, data)
	if err != nil {
		t.Fatalf("UploadFileBytes() error = %v", err)
	}
	if info != "fi-merge" {
		t.Fatalf("file_info = %q", info)
	}

	if prepareBody["file_type"].(float64) != 1 || prepareBody["file_name"] != "a.png" {
		t.Fatalf("prepare body = %+v", prepareBody)
	}
	if prepareBody["file_size"] != "6" {
		t.Fatalf("file_size = %v (want 字符串 \"6\")", prepareBody["file_size"])
	}
	if prepareBody["md5"] != md5HexOf(data) || prepareBody["sha1"] != sha1HexOf(data) || prepareBody["md5_10m"] != md5HexOf(data) {
		t.Fatalf("digests = %+v", prepareBody)
	}

	// 每一片都是原始二进制 PUT（不是 base64、不是 JSON）。
	if len(puts) != 3 || string(puts[0]) != "ab" || string(puts[1]) != "cd" || string(puts[2]) != "ef" {
		t.Fatalf("puts = %q", puts)
	}

	if len(finishes) != 3 {
		t.Fatalf("finish calls = %d", len(finishes))
	}
	for i, body := range finishes {
		if body["upload_id"] != "up-1" || body["part_index"].(float64) != float64(i) || body["block_size"] != "2" {
			t.Fatalf("finish[%d] = %+v", i, body)
		}
		if body["md5"] != md5HexOf(puts[i]) {
			t.Fatalf("finish[%d] md5 = %v", i, body["md5"])
		}
	}

	if mergeBody["upload_id"] != "up-1" || mergeBody["file_name"] != "a.png" {
		t.Fatalf("merge body = %+v", mergeBody)
	}
	if _, ok := mergeBody["url"]; ok {
		t.Fatalf("merge body 不应带 url: %+v", mergeBody)
	}
	if _, ok := mergeBody["file_data"]; ok {
		t.Fatalf("merge body 不应带 file_data: %+v", mergeBody)
	}
}

// TestUploadFileBytesValidates 参数错误应在发请求前失败。
func TestUploadFileBytesValidates(t *testing.T) {
	calls := 0
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
	})
	c := newClient(t, srv, token.NewMemory())
	ctx := context.Background()
	if _, err := c.UploadFileBytes(ctx, event.SceneGroup, "g1", "", 1, []byte("x")); err == nil {
		t.Fatal("空 file_name 未报错")
	}
	if _, err := c.UploadFileBytes(ctx, event.SceneGroup, "g1", "a.png", 1, nil); err == nil {
		t.Fatal("空数据未报错")
	}
	if _, err := c.UploadFileBytes(ctx, 99, "g1", "a.png", 1, []byte("x")); err == nil {
		t.Fatal("未知场景未报错")
	}
	if calls != 0 {
		t.Fatalf("参数错误仍发了 %d 个请求", calls)
	}
}

// TestUploadFileBytesPartMismatch 分片总大小与文件长度不一致时报错。
func TestUploadFileBytesPartMismatch(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case strings.HasSuffix(r.URL.Path, "/upload_prepare"):
			writeJSON(w, 200, `{"upload_id":"up-1","block_size":"2","parts":[
				{"index":0,"presigned_url":"`+serverURL(r)+`/put/0","block_size":"2"},
				{"index":1,"presigned_url":"`+serverURL(r)+`/put/1","block_size":"2"}]}`)
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/upload_part_finish"):
			writeJSON(w, 200, `{}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())
	if _, err := c.UploadFileBytes(context.Background(), event.SceneGroup, "g1", "a.png", 1, []byte("abcde")); err == nil {
		t.Fatal("分片总大小不匹配未报错")
	}
}

// TestPutPartRetryAndReject 验证 PUT 分片：5xx 重试、4xx 不重试。
func TestPutPartRetryAndReject(t *testing.T) {
	attempts := 0
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	c := newClient(t, srv, token.NewMemory())
	if err := c.PutPart(context.Background(), srv.URL+"/put/0", []byte("x")); err != nil {
		t.Fatalf("PutPart(retry) error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}

	attempts = 0
	srv2 := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
	})
	c2 := newClient(t, srv2, token.NewMemory())
	if err := c2.PutPart(context.Background(), srv2.URL+"/put/0", []byte("x")); err == nil {
		t.Fatal("PutPart(404) error = nil")
	}
	if attempts != 1 {
		t.Fatalf("404 attempts = %d, want 1（不重试）", attempts)
	}
}

// TestUploadPrepareErrors 验证预上传响应异常处理。
func TestUploadPrepareErrors(t *testing.T) {
	cases := []struct{ name, body string }{
		{"缺 upload_id", `{"block_size":"2","parts":[{"index":0,"presigned_url":"http://x/0","block_size":"2"}]}`},
		{"block_size 非法", `{"upload_id":"up","block_size":"abc","parts":[]}`},
		{"part block_size 非法", `{"upload_id":"up","block_size":"2","parts":[{"index":0,"presigned_url":"http://x/0","block_size":"x"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/app/getAppAccessToken" {
					writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
					return
				}
				writeJSON(w, 200, tc.body)
			})
			c := newClient(t, srv, token.NewMemory())
			if _, err := c.UploadFileBytes(context.Background(), event.SceneGroup, "g1", "a.png", 1, []byte("abc")); err == nil {
				t.Fatalf("%s: error = nil", tc.name)
			}
		})
	}
}

// serverURL 从请求反推测试服务地址（httptest URL 在闭包里拿不到时用）。
func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// sha1HexOf 计算测试用 SHA1（仅断言用，非密码学用途）。
func sha1HexOf(data []byte) string {
	sum := sha1.Sum(data) // #nosec G401 -- 测试断言
	return hex.EncodeToString(sum[:])
}
