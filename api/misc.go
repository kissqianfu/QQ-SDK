package api

// misc.go 是"机器人"域的零散接口：分享链接等不属于消息/群管理/菜单的小能力。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"
)

// maxCallbackData 是 share link 的 callback_data 上限（官方 32 字符）。
const maxCallbackData = 32

// GenerateURLLink 生成机器人分享链接（POST /v2/generate_url_link，50 QPS）。
//
// callbackData 是开发者自定义透传串（≤32 字符，可为空）；用户通过该链接添加机器人后，
// FRIEND_ADD 事件的 scene_param 会带回这个值，用于区分邀请来源。
func (c *Client) GenerateURLLink(ctx context.Context, callbackData string) (string, error) {
	if n := utf8.RuneCountInString(callbackData); n > maxCallbackData {
		return "", fmt.Errorf("api: callback_data 最多 %d 字符（当前 %d）", maxCallbackData, n)
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]string{}
	if callbackData != "" {
		body["callback_data"] = callbackData
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+"/v2/generate_url_link", tok, body)
	if err != nil {
		return "", fmt.Errorf("api: generate url link: %w", err)
	}
	var out struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
		URL string `json:"url"` // 兼容扁平响应形态
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("api: decode url link: %w", err)
	}
	link := out.Data.URL
	if link == "" {
		link = out.URL
	}
	if link == "" {
		return "", fmt.Errorf("api: generate url link: 响应缺少 url")
	}
	return link, nil
}
