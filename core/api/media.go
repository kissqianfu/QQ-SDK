package api

import (
	"bytes"
	"context"
	"crypto/md5"  // #nosec G501 -- QQ /files 协议要求 MD5 校验和，仅用于文件完整性，不是密码学用途
	"crypto/sha1" // #nosec G505 -- QQ /files 协议要求 SHA1 校验和，仅用于文件完整性，不是密码学用途
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

const (
	// maxUploadBytes 是富媒体硬限制（超过 200MB 报错；超过各类型软限制会被平台降级为文件）。
	maxUploadBytes = 200 << 20
	// maxFileDataBytes 是 file_data 二进制直传上限（官方文档：5M 以内）。
	maxFileDataBytes = 5 << 20
	// md5TenMLimit 是 upload_prepare 要求 md5_10m 的取样长度（前 10002432 字节）。
	md5TenMLimit = 10002432
	// uploadTimeout 是单个分片 PUT 的硬上限（慢链路可调大 config.HTTP，但不会永久挂起）。
	uploadTimeout = 10 * time.Minute
)

// UploadPrepareRequest 是分片上传预申请参数（/v2/{scene}/{openid}/upload_prepare）。
type UploadPrepareRequest struct {
	FileType int
	FileSize int64
	FileName string
	MD5      string
	SHA1     string
	MD5TenM  string
}

// UploadPart 是一个分片：Index 从 0 开始，PresignedURL 用 HTTP PUT 直传原始二进制。
type UploadPart struct {
	Index        int
	PresignedURL string
	BlockSize    int64
}

// UploadPrepareResult 是预上传响应。
type UploadPrepareResult struct {
	UploadID  string
	BlockSize int64
	Parts     []UploadPart
	// Concurrency 是平台建议的并发度（仅作参考）。
	Concurrency int
}

// UploadResult 是 /files 接口的完整响应。
type UploadResult struct {
	// FileInfo 是发送消息 media.file_info 的透传值（平台要求不解析直接透传）。
	FileInfo string
	// FileUUID 是文件唯一 ID。
	FileUUID string
	// TTL 是 file_info 有效期（秒）；0 表示可长期使用，到期需重新上传。
	TTL int
	// ID 是消息 ID（仅 srv_send_msg=true 时返回）。
	ID string
	// RawURL 是文件 COS 下载链接（仅分片合并路径且为图/视频/语音时返回）。
	RawURL string
}

// UploadFile 通过 URL 上传富媒体（file_type: 1=图片 2=视频 3=语音 4=文件），返回 file_info。
// 平台自己拉取 URL；本地文件请用 UploadFileBytes（大文件）或 UploadFileData（≤5M）。
func (c *Client) UploadFile(ctx context.Context, scene event.Scene, targetID, fileURL string, fileType int) (string, error) {
	res, err := c.UploadFileDetail(ctx, scene, targetID, fileURL, fileType)
	if err != nil {
		return "", err
	}
	return res.FileInfo, nil
}

// UploadFileDetail 同 UploadFile，返回完整响应（含 ttl、file_uuid）。
func (c *Client) UploadFileDetail(ctx context.Context, scene event.Scene, targetID, fileURL string, fileType int) (*UploadResult, error) {
	if fileURL == "" {
		return nil, fmt.Errorf("api: upload url 不能为空")
	}
	if u, err := url.Parse(fileURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("api: upload url 必须是 http(s) 绝对地址（当前 %q）", fileURL)
	}
	if err := validateFileType(fileType); err != nil {
		return nil, err
	}
	return c.uploadMediaDetail(ctx, scene, targetID, map[string]any{
		"file_type":    fileType,
		"url":          fileURL,
		"srv_send_msg": false,
	})
}

// UploadFileData 本地小文件（≤5M）base64 直传，不依赖公网 URL；大文件请用 UploadFileBytes。
func (c *Client) UploadFileData(ctx context.Context, scene event.Scene, targetID, fileName string, fileType int, data []byte) (string, error) {
	res, err := c.UploadFileDataDetail(ctx, scene, targetID, fileName, fileType, data)
	if err != nil {
		return "", err
	}
	return res.FileInfo, nil
}

// UploadFileDataDetail 同 UploadFileData，返回完整响应。
func (c *Client) UploadFileDataDetail(ctx context.Context, scene event.Scene, targetID, fileName string, fileType int, data []byte) (*UploadResult, error) {
	if fileName == "" {
		return nil, fmt.Errorf("api: upload file_name 不能为空")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("api: upload 数据不能为空")
	}
	if len(data) > maxFileDataBytes {
		return nil, fmt.Errorf("api: file_data 直传上限 %d 字节，请改用 UploadFileBytes 分片上传", int64(maxFileDataBytes))
	}
	if err := validateFileType(fileType); err != nil {
		return nil, err
	}
	return c.uploadMediaDetail(ctx, scene, targetID, map[string]any{
		"file_type":    fileType,
		"file_name":    fileName,
		"file_data":    base64.StdEncoding.EncodeToString(data),
		"srv_send_msg": false,
	})
}

// uploadMediaDetail 调 POST /files 并解析完整响应（URL、file_data、upload_id 合并共用）。
func (c *Client) uploadMediaDetail(ctx context.Context, scene event.Scene, targetID string, body map[string]any) (*UploadResult, error) {
	path, err := scenePath(scene, targetID, "/files")
	if err != nil {
		return nil, err
	}
	raw, err := c.doJSON(ctx, http.MethodPost, path, body, "upload file")
	if err != nil {
		return nil, err
	}
	var resp struct {
		FileUUID string `json:"file_uuid"`
		FileInfo string `json:"file_info"`
		TTL      int    `json:"ttl"`
		ID       string `json:"id"`
		RawURL   string `json:"raw_url"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("api: decode upload: %w", err)
	}
	if resp.FileInfo == "" {
		return nil, fmt.Errorf("api: upload 响应缺少 file_info")
	}
	return &UploadResult{
		FileInfo: resp.FileInfo,
		FileUUID: resp.FileUUID,
		TTL:      resp.TTL,
		ID:       resp.ID,
		RawURL:   resp.RawURL,
	}, nil
}

// UploadPrepare 申请分片上传，返回 upload_id、分片大小与各分片预签名 URL。
func (c *Client) UploadPrepare(ctx context.Context, scene event.Scene, targetID string, req UploadPrepareRequest) (*UploadPrepareResult, error) {
	if err := validatePrepare(req); err != nil {
		return nil, err
	}
	path, err := scenePath(scene, targetID, "/upload_prepare")
	if err != nil {
		return nil, err
	}
	raw, err := c.doJSON(ctx, http.MethodPost, path, map[string]any{
		"file_type": req.FileType,
		"file_size": strconv.FormatInt(req.FileSize, 10),
		"file_name": req.FileName,
		"md5":       req.MD5,
		"sha1":      req.SHA1,
		"md5_10m":   req.MD5TenM,
	}, "upload prepare")
	if err != nil {
		return nil, err
	}
	return decodePrepare(raw)
}

// validatePrepare 校验 prepare 入参（纯计算，不打平台）。
func validatePrepare(req UploadPrepareRequest) error {
	if req.FileName == "" {
		return fmt.Errorf("api: upload file_name 不能为空")
	}
	if err := validateFileType(req.FileType); err != nil {
		return err
	}
	// 官方 prepare 要求三个校验和全部提供（md5 / sha1 / md5_10m，均为 16 进制串）。
	for _, sum := range []struct{ field, digest string }{
		{"md5", req.MD5},
		{"sha1", req.SHA1},
		{"md5_10m", req.MD5TenM},
	} {
		if err := validateDigest(sum.field, sum.digest); err != nil {
			return err
		}
	}
	if req.FileSize <= 0 {
		return fmt.Errorf("api: upload file_size 必须 > 0")
	}
	if req.FileSize > maxUploadBytes {
		return fmt.Errorf("api: upload 文件超过平台硬限制 %d 字节", int64(maxUploadBytes))
	}
	return nil
}

// decodePrepare 把 prepare 响应归一成结果：字符串尺寸转字节、分片按 index 排序。
func decodePrepare(raw []byte) (*UploadPrepareResult, error) {
	var resp struct {
		UploadID   string `json:"upload_id"`
		BlockSize  string `json:"block_size"`
		UploadConf struct {
			Concurrency int `json:"concurrency"`
		} `json:"upload_config"`
		Parts []struct {
			Index        int    `json:"index"`
			PresignedURL string `json:"presigned_url"`
			BlockSize    string `json:"block_size"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("api: decode upload prepare: %w", err)
	}
	result := &UploadPrepareResult{UploadID: resp.UploadID, Concurrency: resp.UploadConf.Concurrency}
	var err error
	if result.BlockSize, err = parseUploadSize(resp.BlockSize); err != nil {
		return nil, fmt.Errorf("api: upload prepare block_size: %w", err)
	}
	for _, p := range resp.Parts {
		size, err := parseUploadSize(p.BlockSize)
		if err != nil {
			return nil, fmt.Errorf("api: upload prepare part %d block_size: %w", p.Index, err)
		}
		result.Parts = append(result.Parts, UploadPart{Index: p.Index, PresignedURL: p.PresignedURL, BlockSize: size})
	}
	sort.Slice(result.Parts, func(i, j int) bool { return result.Parts[i].Index < result.Parts[j].Index })
	return result, nil
}

// errPartPermanent 标记不可重试的分片 PUT 错误（4xx）。
var errPartPermanent = errors.New("api: upload part 不可重试")

// PutPart 把一片原始二进制 PUT 到预签名 URL（COS 直传，不带 QQ 鉴权头）。
func (c *Client) PutPart(ctx context.Context, presignedURL string, data []byte) error {
	if presignedURL == "" {
		return fmt.Errorf("api: upload part presigned_url 不能为空")
	}
	if len(data) == 0 {
		return fmt.Errorf("api: upload part 数据不能为空")
	}
	return c.runWithRetry(ctx, func(int) (bool, time.Duration, error) {
		if _, err := c.putOnce(ctx, presignedURL, "", data); err != nil {
			if errors.Is(err, errPartPermanent) {
				return false, 0, fmt.Errorf("api: put part: %w", err)
			}
			return true, 0, fmt.Errorf("api: put part: %w", err)
		}
		return false, 0, nil
	})
}

// putOnce 发一次二进制 PUT；4xx 标记为不可重试，2xx 返回响应头。
func (c *Client) putOnce(ctx context.Context, presignedURL, contentType string, data []byte) (http.Header, error) {
	// 预签名地址由平台响应下发：默认只接受 https；http 仅放行本机回环（测试/本地代理）。
	if err := checkPresignedURL(presignedURL); err != nil {
		return nil, fmt.Errorf("%w: %v", errPartPermanent, err)
	}
	// 单次 PUT 的硬上限。
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("api: build put request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := c.uploadHTTP().Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.Header, nil
	}
	if !isRetryableStatus(resp.StatusCode) {
		return nil, fmt.Errorf("%w: HTTP %d", errPartPermanent, resp.StatusCode)
	}
	return nil, fmt.Errorf("api: put: HTTP %d", resp.StatusCode)
}

// checkPresignedURL 校验平台下发的预签名地址：必须是绝对地址，且 https（回环地址可 http）。
func checkPresignedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("预签名地址不是绝对地址")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		switch u.Hostname() {
		case "127.0.0.1", "::1", "localhost":
			return nil
		}
	}
	return fmt.Errorf("预签名地址必须是 https 绝对地址（当前 %q）", u.Scheme)
}

// FinishPart 通知平台某分片已上传完成（每片 PUT 成功后调用）。
func (c *Client) FinishPart(ctx context.Context, scene event.Scene, targetID, uploadID string, index int, blockSize int64, md5Hex string) error {
	if uploadID == "" {
		return fmt.Errorf("api: upload_id 不能为空")
	}
	path, err := scenePath(scene, targetID, "/upload_part_finish")
	if err != nil {
		return err
	}
	body := map[string]any{
		"upload_id":  uploadID,
		"part_index": index,
		"block_size": strconv.FormatInt(blockSize, 10),
		"md5":        md5Hex,
	}
	return c.requestJSON(ctx, http.MethodPost, path, body, "upload part finish", nil)
}

// MergeUpload 携带 upload_id 调 /files 完成合并，返回 file_info。
func (c *Client) MergeUpload(ctx context.Context, scene event.Scene, targetID, fileName string, fileType int, uploadID string) (string, error) {
	if fileName == "" || uploadID == "" {
		return "", fmt.Errorf("api: 合并上传需要 file_name 与 upload_id")
	}
	if err := validateFileType(fileType); err != nil {
		return "", err
	}
	res, err := c.uploadMediaDetail(ctx, scene, targetID, map[string]any{
		"file_type":    fileType,
		"file_name":    fileName,
		"srv_send_msg": false,
		"upload_id":    uploadID,
	})
	if err != nil {
		return "", err
	}
	return res.FileInfo, nil
}

// UploadFileBytes 走官方分片上传完整链路：
// 校验和 → upload_prepare → 逐片 PUT → upload_part_finish → 合并。
// 适用于本地文件；≤5M 小文件用 UploadFileData 更简单。
func (c *Client) UploadFileBytes(ctx context.Context, scene event.Scene, targetID, fileName string, fileType int, data []byte) (string, error) {
	if fileName == "" {
		return "", fmt.Errorf("api: upload file_name 不能为空")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("api: upload 数据不能为空")
	}
	if int64(len(data)) > maxUploadBytes {
		return "", fmt.Errorf("api: upload 文件超过平台硬限制 %d 字节", int64(maxUploadBytes))
	}
	// 先校验类型与场景，再算校验和。
	if err := validateFileType(fileType); err != nil {
		return "", err
	}
	if _, err := scenePath(scene, targetID, "/upload_prepare"); err != nil {
		return "", err
	}
	md5Hex, sha1Hex, md5TenMHex := uploadDigests(data)
	prep, err := c.UploadPrepare(ctx, scene, targetID, UploadPrepareRequest{
		FileType: fileType,
		FileSize: int64(len(data)),
		FileName: fileName,
		MD5:      md5Hex,
		SHA1:     sha1Hex,
		MD5TenM:  md5TenMHex,
	})
	if err != nil {
		return "", err
	}
	if prep.UploadID == "" || len(prep.Parts) == 0 {
		return "", errors.New("api: upload_prepare 响应缺少 upload_id/parts")
	}
	offset := int64(0)
	for _, part := range prep.Parts {
		size := part.BlockSize
		if size <= 0 {
			size = prep.BlockSize
		}
		if size <= 0 {
			return "", fmt.Errorf("api: 分片大小非法（part=%d size=%d offset=%d 总长=%d）", part.Index, size, offset, len(data))
		}
		remaining := int64(len(data)) - offset
		if size > remaining {
			// 末片可能按整块下发（大于剩余字节）：钳到剩余长度，内容由 FinishPart 的 MD5 判定。
			if remaining <= 0 {
				return "", fmt.Errorf("api: 分片超出文件长度（part=%d size=%d offset=%d 总长=%d）", part.Index, size, offset, len(data))
			}
			size = remaining
		}
		chunk := data[offset : offset+size]
		if err := c.PutPart(ctx, part.PresignedURL, chunk); err != nil {
			return "", err
		}
		if err := c.FinishPart(ctx, scene, targetID, prep.UploadID, part.Index, size, md5HexOf(chunk)); err != nil {
			return "", err
		}
		offset += size
	}
	if offset != int64(len(data)) {
		return "", fmt.Errorf("api: 分片总大小 %d 与文件长度 %d 不一致", offset, len(data))
	}
	return c.MergeUpload(ctx, scene, targetID, fileName, fileType, prep.UploadID)
}

// validateFileType 校验富媒体类型：1=图片 2=视频 3=语音 4=文件。
func validateFileType(fileType int) error {
	switch fileType {
	case 1, 2, 3, 4:
		return nil
	}
	return fmt.Errorf("api: file_type 仅支持 1(图片)/2(视频)/3(语音)/4(文件)，当前 %d", fileType)
}

// validateDigest 校验 prepare 的校验和字段：必填且为 16 进制串。
func validateDigest(field, digest string) error {
	if digest == "" {
		return fmt.Errorf("api: upload_prepare 的 %s 必填", field)
	}
	if len(digest)%2 != 0 {
		return fmt.Errorf("api: upload_prepare 的 %s 不是合法 16 进制串", field)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("api: upload_prepare 的 %s 不是合法 16 进制串: %w", field, err)
	}
	return nil
}

// parseUploadSize 解析响应里的字符串字节数。
func parseUploadSize(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("非法字节数 %q: %w", raw, err)
	}
	return size, nil
}

// uploadDigests 计算平台要求的三个校验和（仅用于文件完整性校验，非密码学用途）。
func uploadDigests(data []byte) (md5Hex, sha1Hex, md5TenMHex string) {
	md5Sum := md5.Sum(data)   // #nosec G401 -- 平台协议字段
	sha1Sum := sha1.Sum(data) // #nosec G401 -- 平台协议字段
	prefix := data
	if len(prefix) > md5TenMLimit {
		prefix = prefix[:md5TenMLimit]
	}
	tenSum := md5.Sum(prefix) // #nosec G401 -- 平台协议字段 md5_10m
	return hex.EncodeToString(md5Sum[:]), hex.EncodeToString(sha1Sum[:]), hex.EncodeToString(tenSum[:])
}

// md5HexOf 计算单片的 MD5（upload_part_finish 的 md5 字段）。
func md5HexOf(data []byte) string {
	sum := md5.Sum(data) // #nosec G401 -- 平台协议字段
	return hex.EncodeToString(sum[:])
}
