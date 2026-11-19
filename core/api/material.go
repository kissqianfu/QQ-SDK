package api

// material.go 是素材管理域：Markdown 图片预上传换取 qqbot-media:// 引用，避免公网图床。
// 注意与 media.go 的 /files 富媒体链路不是一条链路（那里产出 file_info）。

import (
	"context"
	"crypto/md5" // #nosec G501 -- 平台素材协议要求 MD5 作为内容指纹，非密码学用途
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	// maxMaterialBytes 是单张素材图片上限（官方 10MB）。
	maxMaterialBytes = 10 << 20
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
	body := map[string]any{
		"file_name":    fileName,
		"file_size":    len(data),
		"content_type": contentType,
		"md5":          md5Hex,
		"sha1":         sha1Hex,
	}
	var out MaterialUploadTicket
	if err := c.requestJSON(ctx, http.MethodPost, materialPreparePath, body, "material prepare", &out); err != nil {
		return nil, err
	}
	if out.UploadID == "" || out.PresignedURL == "" {
		return nil, fmt.Errorf("api: material prepare 响应缺少 upload_id/presigned_url")
	}
	return &out, nil
}

// MaterialPut 用预签名 URL 直传图片二进制，返回 COS ETag（去掉引号，即文件 MD5）。
// Content-Type 必须与 prepare 时声明的一致，否则 COS 返回 403；传输错误/5xx 重试。
func (c *Client) MaterialPut(ctx context.Context, presignedURL, contentType string, data []byte) (string, error) {
	if presignedURL == "" {
		return "", fmt.Errorf("api: material presigned_url 不能为空")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("api: material 数据不能为空")
	}
	var header http.Header
	err := c.runWithRetry(ctx, func(int) (bool, time.Duration, error) {
		h, perr := c.putOnce(ctx, presignedURL, contentType, data)
		header = h
		if perr == nil {
			return false, 0, nil
		}
		if errors.Is(perr, errPartPermanent) {
			return false, 0, perr
		}
		return true, 0, perr
	})
	if err != nil {
		return "", fmt.Errorf("api: material put: %w", err)
	}
	// ETag 可能是裸 md5 或带引号形态；缺失时退回本地计算的 md5（单次 PUT 的 ETag 即内容 MD5）。
	etag := strings.Trim(header.Get("ETag"), `"`)
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
	var out MaterialImage
	if err := c.requestJSON(ctx, http.MethodPost, materialCompletePath,
		map[string]string{"upload_id": uploadID, "md5": md5Hex}, "material complete", &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, fmt.Errorf("api: material complete 响应缺少 url")
	}
	return &out, nil
}

// UploadMaterial 一次完成素材上传三连（prepare → PUT → complete），返回图片引用。
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
	var out MaterialList
	if err := c.getJSON(ctx, mediaPath, pageQuery(cursor, 0, 0), &out); err != nil {
		return nil, fmt.Errorf("api: list materials: %w", err)
	}
	return &out, nil
}

// ListMaterialsAll 自动翻页拉取全部素材（护栏见 paging.go）。
func (c *Client) ListMaterialsAll(ctx context.Context) ([]MaterialImage, error) {
	return paginate(func(cursor string) ([]MaterialImage, string, error) {
		res, err := c.ListMaterials(ctx, cursor)
		if err != nil {
			return nil, "", err
		}
		return res.Images, res.NextCursor, nil
	})
}

// DeleteMaterial 删除素材图片并释放配额（DELETE /v2/media-images/{image_id}/delete）。
// 删除后引用链接立即失效；已发送的消息不受影响。不存在返回 40035008。
func (c *Client) DeleteMaterial(ctx context.Context, imageID string) error {
	if imageID == "" {
		return fmt.Errorf("api: image_id 不能为空")
	}
	return c.requestJSON(ctx, http.MethodDelete, materialPath(imageID, "/delete"), nil, "delete material "+imageID, nil)
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

// materialDigests 计算 prepare 需要的 md5 与 sha1（复用 uploadDigests，少一份等价实现）。
func materialDigests(data []byte) (md5Hex, sha1Hex string) {
	md5Hex, sha1Hex, _ = uploadDigests(data)
	return md5Hex, sha1Hex
}
