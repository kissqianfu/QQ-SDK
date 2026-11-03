package api

// http.go 是 HTTP 传输细节模块：单次请求、重试骨架、退避、错误体解析。
// client.go 只留构造与生命周期；token 刷新在 token.go。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kissqianfu/QQ-SDK/apierr"
	"github.com/kissqianfu/QQ-SDK/token"
)

// errBodyTooLarge 表示响应体超过 maxBody：显式报错，不静默截断成"解析失败"。
var errBodyTooLarge = errors.New("api: 响应体超过上限")

const (
	// userAgent 是统一 UA（平台用于识别接入方）。
	userAgent = "qqbot-sdk/1.0"
	// maxBody 限制响应体读取，防异常大响应打爆内存。
	maxBody = 4 << 20
	// maxAttempts 是默认 REST 重试次数（Config.Retry 可覆盖）。
	maxAttempts = 3
	// retryBase 是默认重试退避基数。
	retryBase = 100 * time.Millisecond
	// maxRetryAfter 是 429 Retry-After 的默认等待上限（防异常头打挂调用方）。
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
	for attempt := 1; attempt <= c.retryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 1 {
			delay := c.retryBase * time.Duration(attempt-1)
			if retryAfter > 0 {
				// 服务器明确给了等待时长就精确遵守，不加抖动。
				delay = retryAfter
			} else {
				// 自己的指数退避加 0~20% 抖动：多实例同时 5xx 时避免同步重试尖峰。
				delay = withJitter(delay)
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

// withJitter 给退避时长加 0~20% 抖动。
func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	span := int64(d / 5)
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(span+1)) // #nosec G404 -- 仅用于退避抖动，非安全用途
}

// do 发一次带重试的请求：429/网络错误总是重试；5xx 仅对幂等方法重试，
// 非幂等 POST（发消息、创建面板等）默认不重试——平台可能在返回 5xx 前已经处理成功，
// 盲目重发会造成重复消息/重复资源。需要旧行为时显式开 Config.RetryNonIdempotent。
//
// 401 特殊处理：平台可能提前吊销 access_token（轮换 AppSecret 等）。若 token 存储
// 实现 token.Deleter，则失效缓存 → 重新取 token → 重试一次（仅一次）。
func (c *Client) do(ctx context.Context, method, url, tok string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("api: encode body: %w", err)
		}
		payload = encoded
	}

	idempotent := idempotentMethod(method)
	var result []byte
	authRetried := false
	err := c.runWithRetry(ctx, func(int) (bool, time.Duration, error) {
		raw, err := c.once(ctx, method, url, tok, payload)
		if err != nil {
			// 响应体超限是确定性错误，重试也一样。
			if errors.Is(err, errBodyTooLarge) {
				return false, 0, err
			}
			// 传输层错误：连接重置/超时/读响应失败时，请求可能**已经到达平台并被处理**，
			// 只是响应丢了。非幂等请求重发会造成重复消息/重复资源，因此默认不重试。
			if !idempotent && !c.retryNonIdempotent && ctx.Err() == nil {
				return false, 0, err
			}
			return true, 0, err
		}
		// 401：token 可能已失效，失效缓存后换新 token 重试一次。
		if raw.status == http.StatusUnauthorized && tok != "" && !authRetried {
			authRetried = true
			if d, ok := c.tokens.(token.Deleter); ok {
				if derr := d.Delete(ctx); derr != nil && c.log != nil {
					c.log.Warn("qqbot api token 失效失败", "err", derr)
				}
				if newTok, terr := c.Token(ctx); terr == nil {
					tok = newTok
					return true, 0, parseAPIError(raw.body, raw.status)
				}
			}
			return false, 0, parseAPIError(raw.body, raw.status)
		}
		if isRetryableStatus(raw.status) {
			// 5xx 对非幂等请求不重试（429 始终可重试：请求肯定没被处理）。
			if raw.status >= 500 && !idempotent && !c.retryNonIdempotent {
				return false, 0, parseAPIError(raw.body, raw.status)
			}
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

// idempotentMethod 判断 HTTP 方法是否幂等（可安全重发）。
func idempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
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

	// 多读 1 字节用于判断是否超限：超限必须显式报错，不能静默截断成"JSON 解析失败"。
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("api: read body: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%w: 超过 %d 字节", errBodyTooLarge, maxBody)
	}
	return &httpResult{status: resp.StatusCode, body: body, retryAfter: parseRetryAfterLimit(resp.Header.Get("Retry-After"), c.maxRetryAfter)}, nil
}

// parseRetryAfter 解析 Retry-After（默认上限），供测试与默认配置使用。
func parseRetryAfter(raw string) time.Duration { return parseRetryAfterLimit(raw, maxRetryAfter) }

// parseRetryAfterLimit 解析 Retry-After；支持"秒数"与 HTTP-date 两种合法形态
// （RFC 9110 两种都允许，平台历史上用过秒数）。非法返回 0；超过 max 按 max 处理。
func parseRetryAfterLimit(raw string, max time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var d time.Duration
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if sec < 0 {
			return 0
		}
		d = time.Duration(sec) * time.Second
	} else if when, err := http.ParseTime(raw); err == nil {
		d = time.Until(when)
		if d < 0 {
			return 0
		}
	} else {
		return 0
	}
	if max > 0 && d > max {
		return max
	}
	return d
}

// isRetryableStatus 判断 HTTP 状态是否值得重试。
func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// parseAPIError 把平台错误体解析成 APIError；解析不了就只带 HTTP 状态。
//
// ⚠️ 官方 v2 文档（API 调用指南）明确错误字段是 **err_code**，并且要求
// "依据 err_code 判断请求是否失败，不要依赖 message"。历史版本/部分接口用过 code，
// 因此两者都读、err_code 优先。只读 code 会让 apierr 的 IsDuplicate/IsExpired/
// IsAuth/IsRateLimited 全部失效（Code 恒为 0），错误处理静默退化。
func parseAPIError(body []byte, status int) *apierr.APIError {
	var payload struct {
		ErrCode int    `json:"err_code"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil ||
		(payload.ErrCode == 0 && payload.Code == 0 && payload.Message == "") {
		return &apierr.APIError{HTTP: status, Message: strings.TrimSpace(string(body))}
	}
	code := payload.ErrCode
	if code == 0 {
		code = payload.Code
	}
	return &apierr.APIError{Code: code, Message: payload.Message, HTTP: status, TraceID: payload.TraceID}
}
