package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// refreshAhead 是 token 提前过期窗口（提前刷新，避免边界 401）。
const refreshAhead = 90 * time.Second

// refreshTimeout 是单次 token 刷新的硬上限：刷新脱离调用方 ctx，避免一个短超时请求
// 把并发等待方一起判失败，因此必须自带超时。
const refreshTimeout = 15 * time.Second

// refreshFlight 是一次进行中的 token 刷新（手写 singleflight，I/O 在锁外完成）。
type refreshFlight struct {
	done chan struct{}
	tok  string
	err  error
}

// Token 返回有效 access_token：缓存命中直接返回，否则串行刷新（提前 90s 过期）。
func (c *Client) Token(ctx context.Context) (string, error) {
	if tok, ok, err := c.tokens.Get(ctx); err != nil {
		// 注入的 Store（如 Redis）读失败：记日志后走刷新兜底，不静默吞掉。
		if c.log != nil {
			c.log.Warn("api: 读取 token 缓存失败，走刷新兜底", "err", err)
		}
	} else if ok && tok != "" {
		return tok, nil
	}

	c.mu.Lock()
	if c.inflight != nil {
		fl := c.inflight
		c.mu.Unlock()
		// 别的 goroutine 正在刷新：等它的结果，不重复打平台。
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-fl.done:
			// 等待方优先返回自己的 ctx 错误：leader 的短超时不该传染给预算充足的调用方。
			if fl.err != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
			return fl.tok, fl.err
		}
	}
	// 双检：首次 Get 与拿到锁之间，可能有别的 leader 已刷新完成并清掉 inflight。
	if tok, ok, _ := c.tokens.Get(ctx); ok && tok != "" {
		c.mu.Unlock()
		return tok, nil
	}
	fl := &refreshFlight{done: make(chan struct{})}
	c.inflight = fl
	c.mu.Unlock()

	var tok string
	var err error
	// defer 收尾：panic 也要落一个错误并关闭 flight，否则等待方会拿到 ("", nil)。
	defer func() {
		if r := recover(); r != nil {
			tok, err = "", fmt.Errorf("api: token refresh panic: %v", r)
		}
		c.mu.Lock()
		if c.inflight == fl {
			c.inflight = nil
		}
		fl.tok, fl.err = tok, err
		close(fl.done)
		c.mu.Unlock()
	}()
	// 刷新与触发者的 ctx 解耦：调用方超时不应让所有等待方一起失败。
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	tok, err = c.refresh(refreshCtx)
	return tok, err
}

// refresh 向平台换取新 token 并写入缓存（业务失败时 HTTP 仍 200，错误在响应体 code）。
func (c *Client) refresh(ctx context.Context) (string, error) {
	body := map[string]string{"appId": c.appID, "clientSecret": c.secret}
	raw, err := c.do(ctx, http.MethodPost, c.tokenURL, "", body)
	if err != nil {
		return "", fmt.Errorf("api: get token: %w", err)
	}
	var resp struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		Code        *int            `json:"code"`
		Message     string          `json:"message"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("api: decode token: %w", err)
	}
	if resp.Code != nil && *resp.Code != 0 {
		return "", fmt.Errorf("api: get token: code=%d %s", *resp.Code, resp.Message)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("api: empty access_token")
	}
	expiresIn, err := parseExpiresIn(resp.ExpiresIn)
	if err != nil {
		return "", err
	}
	expires := time.Duration(expiresIn) * time.Second
	ttl := expires - refreshAhead
	if ttl <= 0 {
		// expires_in 缺失（0）或已短于提前刷新窗口：兜底一分钟。
		ttl = time.Minute
	}
	// 但不越过真实有效期：expires_in 很短时缓存到真实过期为止，
	// 否则缓存里的"有效"token 会提前变成 401。
	if expires > 0 && ttl > expires {
		ttl = expires
	}
	if err := c.tokens.Set(ctx, resp.AccessToken, ttl); err != nil {
		return "", fmt.Errorf("api: store token: %w", err)
	}
	return resp.AccessToken, nil
}

// parseExpiresIn 兼容 expires_in 是数字或字符串（平台当前返回字符串 "7200"）。
func parseExpiresIn(raw json.RawMessage) (int, error) {
	s := strings.Trim(string(raw), `"`)
	if s == "" || s == "null" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("api: expires_in 非法 %q: %w", s, err)
	}
	return n, nil
}
