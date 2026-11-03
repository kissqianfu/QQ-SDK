package api

// material_test.go 覆盖 2026-10-21 新增的素材管理（qqbot-media:// 链路）。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/kissqianfu/QQ-SDK/token"
)

func TestUploadMaterialChain(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n-fake-image-bytes")
	wantMD5 := func() string { s := md5.Sum(png); return hex.EncodeToString(s[:]) }()

	var prepareBody, completeBody map[string]any
	var putContentType string
	var putBytes []byte
	var base string // prepare 响应里的预签名 URL 需要真实测试服务地址，handler 运行前赋值
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/media-images/prepare":
			_ = json.NewDecoder(r.Body).Decode(&prepareBody)
			writeJSON(w, 200, `{"upload_id":"up-1","presigned_url":"`+base+`/cos/upload","expires_in":3600}`)
		case "/cos/upload":
			if r.Method != http.MethodPut {
				t.Errorf("method = %s", r.Method)
			}
			putContentType = r.Header.Get("Content-Type")
			putBytes, _ = io.ReadAll(r.Body)
			w.Header().Set("ETag", `"`+wantMD5+`"`)
			w.WriteHeader(200)
		case "/v2/media-images/complete":
			_ = json.NewDecoder(r.Body).Decode(&completeBody)
			writeJSON(w, 200, `{"image_id":"img-1","url":"qqbot-media://image/v1/abc","ttl":7776000,"preview_url":"https://cos/preview"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	base = srv.URL
	c := newClient(t, srv, token.NewMemory())

	ticket, err := c.MaterialPrepare(context.Background(), "kline.png", "image/png", png)
	if err != nil {
		t.Fatalf("MaterialPrepare() error = %v", err)
	}
	if ticket.UploadID != "up-1" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if prepareBody["md5"] != wantMD5 || prepareBody["sha1"] == "" {
		t.Fatalf("prepare body = %+v", prepareBody)
	}
	if prepareBody["content_type"] != "image/png" || prepareBody["file_name"] != "kline.png" {
		t.Fatalf("prepare body = %+v", prepareBody)
	}

	etag, err := c.MaterialPut(context.Background(), ticket.PresignedURL, "image/png", png)
	if err != nil {
		t.Fatalf("MaterialPut() error = %v", err)
	}
	if etag != wantMD5 {
		t.Fatalf("etag = %q, want %q", etag, wantMD5)
	}
	if putContentType != "image/png" {
		t.Fatalf("PUT Content-Type = %q, want image/png", putContentType)
	}
	if string(putBytes) != string(png) {
		t.Fatal("PUT 上传的二进制不一致")
	}

	img, err := c.MaterialComplete(context.Background(), ticket.UploadID, etag)
	if err != nil {
		t.Fatalf("MaterialComplete() error = %v", err)
	}
	if img.ImageID != "img-1" || img.URL != "qqbot-media://image/v1/abc" || img.TTL != 7776000 {
		t.Fatalf("image = %+v", img)
	}
	if completeBody["upload_id"] != "up-1" || completeBody["md5"] != etag {
		t.Fatalf("complete body = %+v", completeBody)
	}
}

// TestMaterialValidationAndListDelete 覆盖本地校验与列表/删除。
func TestMaterialValidationAndListDelete(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case r.URL.Path == "/v2/media-images" && r.Method == http.MethodGet:
			writeJSON(w, 200, `{"images":[{"image_id":"img-1","url":"qqbot-media://image/v1/a","file_name":"a.png","created_at":1,"expires_at":2}],"next_cursor":"","total":1}`)
		case r.URL.Path == "/v2/media-images/img-1/delete":
			if r.Method != http.MethodDelete {
				t.Errorf("method = %s", r.Method)
			}
			writeJSON(w, 200, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	if _, err := c.MaterialPrepare(context.Background(), "a.exe", "image/png", []byte("x")); err == nil {
		t.Fatal("不支持的扩展名应该报错")
	}
	if _, err := c.MaterialPrepare(context.Background(), "a.png", "image/jpeg", []byte("x")); err == nil {
		t.Fatal("扩展名与 Content-Type 不一致应该报错")
	}
	big := make([]byte, maxMaterialBytes+1)
	if _, err := c.MaterialPrepare(context.Background(), "a.png", "image/png", big); err == nil {
		t.Fatal("超过 10MB 应该报错")
	}
	if _, err := c.MaterialPrepare(context.Background(), "a.png", "image/png", nil); err == nil {
		t.Fatal("空数据应该报错")
	}

	list, err := c.ListMaterials(context.Background(), "")
	if err != nil || len(list.Images) != 1 || list.Total != 1 {
		t.Fatalf("ListMaterials() = %+v, %v", list, err)
	}
	if err := c.DeleteMaterial(context.Background(), "img-1"); err != nil {
		t.Fatalf("DeleteMaterial() error = %v", err)
	}
	if err := c.DeleteMaterial(context.Background(), ""); err == nil {
		t.Fatal("空 image_id 应该报错")
	}
}
