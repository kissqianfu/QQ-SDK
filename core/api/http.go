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

	"github.com/kissqianfu/QQ-SDK/common/apierr"
	"github.com/kissqianfu/QQ-SDK/common/token"
	"github.com/kissqianfu/QQ-SDK/common/version"
)

// errBodyTooLarge 表示响应体超过 maxBody：显式报错，不静默截断成"解析失败"。
var errBodyTooLarge = errors.New("api: 响应体超过上限")

// userAgent 是统一 UA（平台用于识别接入方），版本随 VERSION 走。
var userAgent = version.UserAgent()

const (
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

// runWithRetry 是统一的重试骨架：fn 返回 retryable=false 立即返回 err；
// 否则按 retryAfter 或线性退避（base×(attempt-1)）+抖动重试，超上限返回最后一次 err，ctx 取消立即返回。
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

// do 发一次带重试的请求：429/网络错误总是重试；5xx 仅对幂等方法重试（防重复消息）。
// 401 时若 token 存储实现 token.Deleter，则失效缓存换新 token 重试一次。
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
			// 传输层错误时请求可能已到达平台，非幂等请求默认不重试。
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
				} else if c.log != nil {
					// 重取 token 失败的根因不能吞掉，否则只剩一个看不出缘由的 401。
					c.log.Warn("qqbot api 401 后重取 token 失败，按 401 处理", "err", terr)
				}
			}
			return false, 0, parseAPIError(raw.body, raw.status)
		}
		if isRetryableStatus(raw.status) {
			// 非幂等请求（发消息等）不自动重发：5xx 可能已落库，429 也没有"必然未处理"的
			// 契约，重发会造成用户可见的重复消息。需要重发时显式开 RetryNonIdempotent。
			if !idempotent && !c.retryNonIdempotent {
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

// maxRetryAfterSeconds 是 Retry-After 可接受的最大秒数（约 31 年），超出视为异常值取配置上限。
const maxRetryAfterSeconds = int64(1) << 40

// parseRetryAfterLimit 解析 Retry-After（秒数与 HTTP-date 两种形态都支持）；非法返回 0。
func parseRetryAfterLimit(raw string, max time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var d time.Duration
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if sec <= 0 {
			return 0
		}
		// 防溢出：秒数大到无法用 Duration 表示时直接取上限（否则会回绕成负数 → 立刻重试）。
		if sec > maxRetryAfterSeconds {
			if max > 0 {
				return max
			}
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

// pathURL 把相对路径补成完整地址：全包唯一拼 baseURL 的地方。
func (c *Client) pathURL(path string) string {
	return c.baseURL + path
}

// doPath 收敛「Token → do」半段，path 相对 baseURL；不额外包错误，由调用方决定前缀。
func (c *Client) doPath(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		// TokenStore 返回空串也当"成功"时不许发出无鉴权请求：那是必然 401 的哑失败。
		return nil, errors.New("api: 空 access_token")
	}
	return c.do(ctx, method, c.pathURL(path), tok, body)
}

// doJSON 在 doPath 外补一层错误前缀 op。
func (c *Client) doJSON(ctx context.Context, method, path string, body any, op string) ([]byte, error) {
	raw, err := c.doPath(ctx, method, path, body)
	if err != nil {
		return nil, fmt.Errorf("api: %s: %w", op, err)
	}
	return raw, nil
}

// requestJSON = doJSON + decodeJSON：标准「发请求 → 解码响应」；out 为 nil 表示只发不收。
func (c *Client) requestJSON(ctx context.Context, method, path string, body any, op string, out any) error {
	raw, err := c.doJSON(ctx, method, path, body, op)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := decodeJSON(raw, out); err != nil {
		return err
	}
	return nil
}

// parseAPIError 把平台错误体解析成 APIError；err_code 优先（官方口径），code 兜底，
// 只读 code 会让 apierr 的判定方法全部失效。
func parseAPIError(body []byte, status int) *apierr.APIError {
	var payload struct {
		ErrCode int    `json:"err_code"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil ||
		(payload.ErrCode == 0 && payload.Code == 0 && payload.Message == "") {
		return &apierr.APIError{HTTP: status, Message: errExcerpt(body)}
	}
	code := payload.ErrCode
	if code == 0 {
		code = payload.Code
	}
	return &apierr.APIError{Code: code, Message: payload.Message, HTTP: status, TraceID: payload.TraceID}
}

// errExcerptLen 是错误体摘要的最大字节数。
const errExcerptLen = 256

// errExcerpt 把非 JSON 错误体压成单行摘要：剔除控制字符（防日志注入）并截断
// （响应体上限 4MB，原样进错误串会污染日志并放大内存）。
func errExcerpt(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(s), errExcerptLen))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			r = ' '
		case r < 0x20 || r == 0x7f:
			continue // 其余控制字符直接丢弃
		}
		b.WriteRune(r)
		if b.Len() >= errExcerptLen {
			b.WriteString("…")
			break
		}
	}
	return strings.TrimSpace(b.String())
}
