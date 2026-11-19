package api

// misc.go 是"机器人"域的零散接口：分享链接等不属于消息/群管理/菜单的小能力。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/kissqianfu/QQ-SDK/common/apierr"
)

// maxCallbackData 是 share link 的 callback_data 上限（官方 32 字符）。
const maxCallbackData = 32

// GenerateURLLink 生成机器人分享链接（50 QPS）。
// callbackData ≤32 字符；用户添加机器人后 FRIEND_ADD 的 scene_param 会带回该值。
func (c *Client) GenerateURLLink(ctx context.Context, callbackData string) (string, error) {
	if n := utf8.RuneCountInString(callbackData); n > maxCallbackData {
		return "", fmt.Errorf("api: callback_data 最多 %d 字符（当前 %d）", maxCallbackData, n)
	}
	body := map[string]string{}
	if callbackData != "" {
		body["callback_data"] = callbackData
	}
	raw, err := c.doJSON(ctx, http.MethodPost, urlLinkPath, body, "generate url link")
	if err != nil {
		return "", err
	}
	// 响应形态：{"retcode":0,"msg":"success","data":{"url":...}}。
	var out struct {
		Retcode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("api: decode url link: %w", err)
	}
	if out.Retcode != 0 {
		// 用 APIError 而不是裸 error。
		return "", &apierr.APIError{Code: out.Retcode, Message: out.Msg, HTTP: http.StatusOK}
	}
	if out.Data.URL == "" {
		return "", fmt.Errorf("api: generate url link: 响应缺少 url")
	}
	return out.Data.URL, nil
}
