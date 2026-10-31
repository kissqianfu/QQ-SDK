package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// GroupInfo 是群基本信息（GET /v2/groups/{group_openid}/info，30 QPM，白名单机器人可用）。
type GroupInfo struct {
	GroupOpenID    string   `json:"group_openid"`
	GroupName      string   `json:"group_name"`
	GroupFingerMem string   `json:"group_finger_memo"` // 群简介
	GroupClassText string   `json:"group_class_text"`  // 群分类
	GroupTags      []string `json:"group_tags"`        // 群标签列表
	GroupMemberNum int      `json:"group_member_num"`  // 群成员人数
}

// GroupBotState 是机器人在指定群内的状态（GET /v2/groups/{group_openid}/bot_state，30 QPM）。
type GroupBotState struct {
	MemberOpenID      string `json:"member_openid"`
	JoinedAt          string `json:"joined_at"` // 入群时间，RFC3339
	AllowProactiveMsg bool   `json:"allow_proactive_msg"`
	// RecvMsgSetting 接收消息类型：all / only_mention / mention_and_context。
	RecvMsgSetting string `json:"recv_msg_setting"`
	// MemberRole 机器人群成员角色：member 普通成员 / owner 群主 / admin 管理员。
	MemberRole string `json:"member_role"`
}

// GroupRestrictSetting 是群禁言状态（GET /v2/groups/{group_openid}/restrict_chat_setting，
// 30 QPM，机器人需群管理员身份）。
type GroupRestrictSetting struct {
	GlobalRule *GlobalMuteRule   `json:"global_rule"` // 群级禁言规则（全员禁言配置）
	Members    []MemberMuteState `json:"members"`     // 当前禁言中的成员列表（不含已过期）
}

// GlobalMuteRule 是全员禁言规则。
type GlobalMuteRule struct {
	// Mode 全员禁言模式：none 未开启 / always 始终禁言 / schedule 定时禁言。
	Mode           string              `json:"mode"`
	ScheduleRules  []MuteScheduleRule  `json:"schedule_rules"`  // 定时禁言规则
	RecurringRules []MuteRecurringRule `json:"recurring_rules"` // 周期禁言规则
}

// MuteScheduleRule 是一次性的定时禁言规则。
type MuteScheduleRule struct {
	TaskID  string `json:"task_id"` // 任务 ID
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
	Enabled bool   `json:"enabled"`
}

// MuteRecurringRule 是周期性禁言规则。
type MuteRecurringRule struct {
	TaskID    string `json:"task_id"`
	Weekdays  []int  `json:"weekdays"` // 1~7（1=周一，7=周日），可多选
	StartTime string `json:"start_time"`
	// EndTime 为 HH:mm（北京时间）；小于 StartTime 表示跨天到次日。
	EndTime string `json:"end_time"`
	Enabled bool   `json:"enabled"`
}

// MemberMuteState 是单个成员的禁言状态。
type MemberMuteState struct {
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at"` // 禁言到期时间，RFC3339
	Username     string `json:"username"`
	UnionOpenID  string `json:"union_openid"`
}

// GroupInfo 获取指定群的基本信息。
// 该接口仅白名单机器人可用（错误码 11253）。
func (c *Client) GroupInfo(ctx context.Context, groupOpenID string) (*GroupInfo, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	var out GroupInfo
	if err := c.getJSON(ctx, "/v2/groups/"+url.PathEscape(groupOpenID)+"/info", nil, &out); err != nil {
		return nil, fmt.Errorf("api: group info %s: %w", groupOpenID, err)
	}
	return &out, nil
}

// GroupBotState 获取机器人在指定群内的状态。
func (c *Client) GroupBotState(ctx context.Context, groupOpenID string) (*GroupBotState, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	var out GroupBotState
	if err := c.getJSON(ctx, "/v2/groups/"+url.PathEscape(groupOpenID)+"/bot_state", nil, &out); err != nil {
		return nil, fmt.Errorf("api: group bot state %s: %w", groupOpenID, err)
	}
	return &out, nil
}

// GroupRestrictSetting 查询群禁言状态（机器人需拥有群管理员身份）。
func (c *Client) GroupRestrictSetting(ctx context.Context, groupOpenID string) (*GroupRestrictSetting, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	var out GroupRestrictSetting
	if err := c.getJSON(ctx, "/v2/groups/"+url.PathEscape(groupOpenID)+"/restrict_chat_setting", nil, &out); err != nil {
		return nil, fmt.Errorf("api: group restrict setting %s: %w", groupOpenID, err)
	}
	return &out, nil
}

// getJSON 是 GET 请求 + JSON 解码的公共骨架：query 可为 nil。
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	raw, err := c.do(ctx, http.MethodGet, full, tok, nil)
	if err != nil {
		return err
	}
	if err := decodeJSON(raw, out); err != nil {
		return err
	}
	return nil
}
