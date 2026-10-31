package api

// http.go 是 HTTP 传输细节模块：单次请求、重试骨架、退避、错误体解析。
// client.go 只留构造与生命周期；token 刷新在 token.go。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kissqianfu/QQ-SDK/apierr"
)

const (
	// userAgent 是统一 UA（平台用于识别接入方）。
	userAgent = "qqbot-sdk/1.0"
	// maxBody 限制响应体读取，防异常大响应打爆内存。
	maxBody = 4 << 20
	// maxAttempts 是 REST 重试次数（仅 429/5xx/网络错误）。
	maxAttempts = 3
	// retryBase 是重试退避基数。
	retryBase = 100 * time.Millisecond
	// maxRetryAfter 是 429 Retry-After 的等待上限（防异常头打挂调用方）。
	maxRetryAfter = 30 * time.Second
)

// waitCtx 是 ctx 感知的退避等待：ctx 取消立即返回。
func waitCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// runWithRetry 是统一的重试骨架（do / PutPart 共用）：
// fn 是第 attempt 次尝试；err == nil 即成功；retryable=false 立即返回 err；
// 否则按 retryAfter（>0 时）或 retryBase×(attempt-1) 退避后重试，超 maxAttempts 返回最后一次 err。
// 每次尝试前与退避中都检查 ctx，取消立即返回。
func (c *Client) runWithRetry(ctx context.Context, fn func(attempt int) (retryable bool, retryAfter time.Duration, err error)) error {
	var lastErr error
	var retryAfter time.Duration
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 1 {
			delay := retryBase * time.Duration(attempt-1)
			if retryAfter > 0 {
				delay = retryAfter
			}
			if err := c.sleepCtx(ctx, delay); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var err error
		retryable := false
		retryable, retryAfter, err = fn(attempt)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
		if c.log != nil {
			c.log.Warn("qqbot api request failed", "attempt", attempt, "err", err)
		}
	}
	return lastErr
}

// do 发一次带重试的请求：只对 429/5xx/网络错误重试，4xx 立刻返回 apierr.APIError。
// 429 优先用响应的 Retry-After 作为等待时长；ctx 取消后立即停止重试。
func (c *Client) do(ctx context.Context, method, url, tok string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("api: encode body: %w", err)
		}
		payload = encoded
	}

	var result []byte
	err := c.runWithRetry(ctx, func(int) (bool, time.Duration, error) {
		raw, err := c.once(ctx, method, url, tok, payload)
		if err != nil {
			return true, 0, err
		}
		if isRetryableStatus(raw.status) {
			return true, raw.retryAfter, parseAPIError(raw.body, raw.status)
		}
		if raw.status >= 400 {
			return false, 0, parseAPIError(raw.body, raw.status)
		}
		result = raw.body
		return false, 0, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// httpResult 是一次响应的状态与体。
type httpResult struct {
	status     int
	body       []byte
	retryAfter time.Duration // 429 响应的 Retry-After（秒），解析不了为 0
}

// once 发一次 HTTP 请求并读取响应体。
func (c *Client) once(ctx context.Context, method, url, tok string, payload []byte) (*httpResult, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("api: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "QQBot "+tok)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("api: read body: %w", err)
	}
	return &httpResult{status: resp.StatusCode, body: body, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}, nil
}

// parseRetryAfter 解析 Retry-After（秒）；非法或超上限时按上限/0 处理。
func parseRetryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || sec < 0 {
		return 0
	}
	d := time.Duration(sec) * time.Second
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// isRetryableStatus 判断 HTTP 状态是否值得重试。
func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// parseAPIError 把平台错误体解析成 APIError；解析不了就只带 HTTP 状态。
func parseAPIError(body []byte, status int) *apierr.APIError {
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || (payload.Code == 0 && payload.Message == "") {
		return &apierr.APIError{HTTP: status, Message: string(body)}
	}
	return &apierr.APIError{Code: payload.Code, Message: payload.Message, HTTP: status, TraceID: payload.TraceID}
}
