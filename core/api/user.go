package api

import (
	"context"
	"fmt"
)

// UserInfo 是用户基本信息（GET /v2/users/{user_openid}/info，10 QPS）。
// 注意：用户与机器人无好友关系时平台返回 40184001（"用户与机器人无好友关系"）。
type UserInfo struct {
	Username string `json:"username"` // 用户名
	Avatar   string `json:"avatar"`   // 用户头像原图 URL
	// AllowProactiveMsg 是否接收主动推送；true 表示接受主动推送。
	AllowProactiveMsg bool `json:"allow_proactive_msg"`
	// UnionOpenID 跨应用关联用户 openid，当绑定了三方应用时有效（否则为空）。
	UnionOpenID string `json:"union_openid"`
}

// UserInfo 获取指定 openid 的用户基本信息。
func (c *Client) UserInfo(ctx context.Context, userOpenID string) (*UserInfo, error) {
	if userOpenID == "" {
		return nil, fmt.Errorf("api: user_openid 不能为空")
	}
	var out UserInfo
	if err := c.getJSON(ctx, userPath(userOpenID, "/info"), nil, &out); err != nil {
		return nil, fmt.Errorf("api: user info %s: %w", userOpenID, err)
	}
	return &out, nil
}
