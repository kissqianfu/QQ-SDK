package api

// material.go 是"素材管理"域（2026-10-21 文档新增）：把 Markdown 图片预上传到平台，
// 换取 qqbot-media:// 引用，避免依赖公网图床。
//
// ⚠️ 与 media.go 的 /files 富媒体上传**不是一条链路**：
//   - /files 产出 file_info，用于 msg_type=7 富媒体消息；
//   - /media-images 产出 qqbot-media:// 引用，写在 msg_type=2 Markdown 的 content 里。
//
// 四步链路（官方）：
//  1. POST /v2/media-images/prepare   → upload_id + presigned_url
//  2. PUT  <presigned_url>            → 直传二进制（Content-Type 必须与 prepare 一致）
//  3. POST /v2/media-images/complete  → 校验 ETag，返回 image_id + qqbot-media:// url
//  4. GET/DELETE /v2/media-images     → 列表 / 删除（释放 100 张配额）

import (
	"bytes"
	"context"
	"crypto/md5"  // #nosec G501 -- 平台素材协议要求 MD5 作为内容指纹，非密码学用途
	"crypto/sha1" // #nosec G505 -- 平台素材协议要求 SHA1 作为内容指纹，非密码学用途
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	// maxMaterialBytes 是单张素材图片上限（官方 10MB）。
	maxMaterialBytes = 10 << 20
	// materialPrefix 是 /v2/media-images 系列接口的路径前缀。
	materialPrefix = "/v2/media-images"
)

// materialMIME 是支持的图片 MIME（扩展名必须与之一致）。
var materialMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// MaterialUploadTicket 是 prepare 的响应。
type MaterialUploadTicket struct {
	// UploadID 上传任务 ID，4 小时内有效。
	UploadID string `json:"upload_id"`
	// PresignedURL 预签名上传地址（HTTP PUT 直传二进制）。
	PresignedURL string `json:"presigned_url"`
	// ExpiresIn 预签名 URL 有效期（秒）。
	ExpiresIn uint32 `json:"expires_in"`
}

// MaterialImage 是一张已上传的素材图片。
type MaterialImage struct {
	ImageID string `json:"image_id"`
	// URL 是 Markdown 引用链接：qqbot-media://image/v1/<token>。
	URL string `json:"url"`
	// TTL 是滑动有效期窗口（秒，官方 90 天 = 7776000）。
	TTL uint32 `json:"ttl"`
	// PreviewURL 是短时效外网预览链接，不要持久化。
	PreviewURL string `json:"preview_url"`
	// 以下字段仅列表接口返回。
	FileName  string `json:"file_name"`
	CreatedAt int64  `json:"created_at"` // Unix 秒
	ExpiresAt int64  `json:"expires_at"` // Unix 秒
}

// MaterialList 是素材图片列表。
type MaterialList struct {
	Images     []MaterialImage `json:"images"`
	NextCursor string          `json:"next_cursor"`
	Total      uint64          `json:"total"`
}

// MaterialPrepare 申请素材上传（POST /v2/media-images/prepare）。
// 本地先校验格式/扩展名/大小并计算 md5、sha1，省一次 400 往返。
func (c *Client) MaterialPrepare(ctx context.Context, fileName, contentType string, data []byte) (*MaterialUploadTicket, error) {
	if err := validateMaterial(fileName, contentType, data); err != nil {
		return nil, err
	}
	md5Hex, sha1Hex := materialDigests(data)
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"file_name":    fileName,
		"file_size":    len(data),
		"content_type": contentType,
		"md5":          md5Hex,
		"sha1":         sha1Hex,
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+materialPrefix+"/prepare", tok, body)
	if err != nil {
		return nil, fmt.Errorf("api: material prepare: %w", err)
	}
	var out MaterialUploadTicket
	if err := decodeJSON(raw, &out); err != nil {
		return nil, err
	}
	if out.UploadID == "" || out.PresignedURL == "" {
		return nil, fmt.Errorf("api: material prepare 响应缺少 upload_id/presigned_url")
	}
	return &out, nil
}

// MaterialPut 用预签名 URL 直传图片二进制，返回 COS ETag（去掉引号，即文件 MD5）。
//
// Content-Type 必须与 prepare 时声明的完全一致，否则 COS 返回 403。
func (c *Client) MaterialPut(ctx context.Context, presignedURL, contentType string, data []byte) (string, error) {
	if presignedURL == "" {
		return "", fmt.Errorf("api: material presigned_url 不能为空")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("api: material 数据不能为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("api: build material put: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("api: material put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("api: material put: HTTP %d", resp.StatusCode)
	}
	// ETag 可能是裸 md5 或带引号形态；缺失时退回本地计算的 md5（单次 PUT 的 ETag 即内容 MD5）。
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		sum := md5.Sum(data) // #nosec G401 -- 平台协议字段
		etag = hex.EncodeToString(sum[:])
	}
	return etag, nil
}

// MaterialComplete 完成素材上传（POST /v2/media-images/complete）。
// md5Hex 是 MaterialPut 返回的 ETag。
func (c *Client) MaterialComplete(ctx context.Context, uploadID, md5Hex string) (*MaterialImage, error) {
	if uploadID == "" || md5Hex == "" {
		return nil, fmt.Errorf("api: material complete 需要 upload_id 与 md5")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+materialPrefix+"/complete", tok,
		map[string]string{"upload_id": uploadID, "md5": md5Hex})
	if err != nil {
		return nil, fmt.Errorf("api: material complete: %w", err)
	}
	var out MaterialImage
	if err := decodeJSON(raw, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, fmt.Errorf("api: material complete 响应缺少 url")
	}
	return &out, nil
}

// UploadMaterial 一次完成素材上传三连（prepare → PUT → complete），返回图片引用。
//
// 例：
//
//	img, err := c.Sender().UploadMaterial(ctx, "kline.png", "image/png", pngBytes)
//	md := fmt.Sprintf("![K线](%s)", img.URL)
func (c *Client) UploadMaterial(ctx context.Context, fileName, contentType string, data []byte) (*MaterialImage, error) {
	ticket, err := c.MaterialPrepare(ctx, fileName, contentType, data)
	if err != nil {
		return nil, err
	}
	etag, err := c.MaterialPut(ctx, ticket.PresignedURL, contentType, data)
	if err != nil {
		return nil, err
	}
	return c.MaterialComplete(ctx, ticket.UploadID, etag)
}

// ListMaterials 查询有效素材图片（GET /v2/media-images?cursor=...），按上传时间倒序。
func (c *Client) ListMaterials(ctx context.Context, cursor string) (*MaterialList, error) {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out MaterialList
	if err := c.getJSON(ctx, materialPrefix, q, &out); err != nil {
		return nil, fmt.Errorf("api: list materials: %w", err)
	}
	return &out, nil
}

// ListMaterialsAll 自动翻页拉取全部素材（护栏 100 页）。
func (c *Client) ListMaterialsAll(ctx context.Context) ([]MaterialImage, error) {
	var all []MaterialImage
	cursor := ""
	for page := 0; page < 100; page++ {
		res, err := c.ListMaterials(ctx, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Images...)
		if res.NextCursor == "" {
			return all, nil
		}
		if res.NextCursor == cursor {
			return nil, fmt.Errorf("api: list materials: 游标不前进 %q", cursor)
		}
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("api: list materials: 超过最大页数")
}

// DeleteMaterial 删除素材图片并释放配额（DELETE /v2/media-images/{image_id}/delete）。
// 删除后引用链接立即失效；已发送的消息不受影响。不存在返回 40035008。
func (c *Client) DeleteMaterial(ctx context.Context, imageID string) error {
	if imageID == "" {
		return fmt.Errorf("api: image_id 不能为空")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	path := materialPrefix + "/" + url.PathEscape(imageID) + "/delete"
	if _, err := c.do(ctx, http.MethodDelete, c.baseURL+path, tok, nil); err != nil {
		return fmt.Errorf("api: delete material %s: %w", imageID, err)
	}
	return nil
}

// validateMaterial 校验文件名扩展名、MIME 与大小。
func validateMaterial(fileName, contentType string, data []byte) error {
	if strings.TrimSpace(fileName) == "" {
		return fmt.Errorf("api: material file_name 不能为空")
	}
	if len(data) == 0 {
		return fmt.Errorf("api: material 数据不能为空")
	}
	if len(data) > maxMaterialBytes {
		return fmt.Errorf("api: material 超过单图上限 %d 字节（当前 %d）", maxMaterialBytes, len(data))
	}
	ext := strings.ToLower(path.Ext(fileName))
	want, ok := materialMIME[ext]
	if !ok {
		return fmt.Errorf("api: material 仅支持 png/jpeg/gif/webp，当前扩展名 %q", ext)
	}
	if contentType != want {
		return fmt.Errorf("api: material 扩展名 %q 应配 Content-Type %q，当前 %q", ext, want, contentType)
	}
	return nil
}

// materialDigests 计算 prepare 需要的 md5 与 sha1。
func materialDigests(data []byte) (md5Hex, sha1Hex string) {
	m := md5.Sum(data)  // #nosec G401 -- 平台协议字段
	s := sha1.Sum(data) // #nosec G401 -- 平台协议字段
	return hex.EncodeToString(m[:]), hex.EncodeToString(s[:])
}
