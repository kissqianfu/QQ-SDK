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

// Token 返回有效 access_token：缓存命中直接返回，否则串行刷新（提前 90s 过期）。
func (c *Client) Token(ctx context.Context) (string, error) {
	if tok, ok, err := c.tokens.Get(ctx); err != nil {
		return "", err
	} else if ok {
		return tok, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// 等锁期间可能已被别的 goroutine 刷新过。
	if tok, ok, err := c.tokens.Get(ctx); err == nil && ok {
		return tok, nil
	}
	return c.refresh(ctx)
}

// refresh 向平台换取新 token 并写入缓存。
//
// 注意：官方文档明确该接口业务失败时 HTTP 仍是 200，错误在响应体 code/message
// （"请优先依据 code 判断请求是否成功，不要只依赖 HTTP 返回码"）。
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
	ttl := time.Duration(expiresIn)*time.Second - refreshAhead
	if ttl <= 0 {
		ttl = time.Minute
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
