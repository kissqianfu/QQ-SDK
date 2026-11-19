package api

// join_approval.go 是入群自动审批策略接口：命中白名单号码的入群申请自动通过。
// 路径在 /v2/groups/join_approval_strategy 下，不带 {group_openid} 段（与 group_admin.go 的人工审批互补）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 本组接口的取值词表：is_enable 是 on/off 字符串，op 是 add/del（白名单与关联群共用）。
const (
	StrategyEnableOn  = "on"
	StrategyEnableOff = "off"

	ApprovalOpAdd = "add"
	ApprovalOpDel = "del"
)

// 策略协议上限（官方请求体约束）。
const (
	maxStrategyGroups      = 100   // 单个策略关联群数
	maxStrategyRemarkRunes = 255   // 备注字符数
	maxWhitelistPerCall    = 10000 // 单次白名单号码数（策略总量上限 10 万，由平台校验）
)

// GroupIDs 是 QQ 群号列表（真实群号，不是 group_openid）。
// 请求按官方口径发数字；响应可能下发字符串（文档示例即如此），两种形态都能解析。
type GroupIDs []uint64

// UnmarshalJSON 兼容数字与十进制字符串两种元素形态。
func (g *GroupIDs) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("api: group_ids: %w", err)
	}
	out := make(GroupIDs, 0, len(raw))
	for _, item := range raw {
		s := strings.Trim(strings.TrimSpace(string(item)), `"`)
		if s == "" || s == "null" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return fmt.Errorf("api: group_ids 含非数字取值 %q", s)
		}
		out = append(out, n)
	}
	*g = out
	return nil
}

// JoinApprovalStrategy 是一条入群自动审批策略。
type JoinApprovalStrategy struct {
	StrategyID string `json:"strategy_id"`
	// GroupOpenIDs 与 GroupIDs 只有一个非空：按创建时用的群标识形态回填。
	GroupOpenIDs []string `json:"group_openids"`
	GroupIDs     GroupIDs `json:"group_ids"`
	// WhitelistUserCount 是白名单号码数（官方：估算值，可能有少量误差）。
	WhitelistUserCount int    `json:"whitelist_user_count"`
	IsEnable           string `json:"is_enable"`
	ExpireAt           string `json:"expire_at"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	Remark             string `json:"remark"`
}

// Enabled 报告策略是否处于启用状态。
func (s JoinApprovalStrategy) Enabled() bool { return s.IsEnable == StrategyEnableOn }

// JoinApprovalStrategyList 是分页的策略列表（单页最多 50）。
type JoinApprovalStrategyList struct {
	Strategies []JoinApprovalStrategy `json:"strategies"`
	NextCursor string                 `json:"next_cursor"`
}

// ListJoinApprovalStrategies 分页查询生效中的策略（GET /v2/groups/join_approval_strategy，60 QPM）。
// 按创建时间倒序；limit 为 0 用平台默认 20，最大 50。
func (c *Client) ListJoinApprovalStrategies(ctx context.Context, cursor string, limit int) (*JoinApprovalStrategyList, error) {
	if limit < 0 {
		return nil, fmt.Errorf("api: limit 不能为负数")
	}
	var out JoinApprovalStrategyList
	if err := c.getJSON(ctx, strategyBasePath, pageQuery(cursor, limit, 50), &out); err != nil {
		return nil, fmt.Errorf("api: list join approval strategies: %w", err)
	}
	return &out, nil
}

// ListJoinApprovalStrategiesAll 自动翻页拉取全部策略（护栏见 paging.go）。
func (c *Client) ListJoinApprovalStrategiesAll(ctx context.Context) ([]JoinApprovalStrategy, error) {
	return paginate(func(cursor string) ([]JoinApprovalStrategy, string, error) {
		res, err := c.ListJoinApprovalStrategies(ctx, cursor, 0)
		if err != nil {
			return nil, "", err
		}
		return res.Strategies, res.NextCursor, nil
	})
}

// CreateJoinApprovalStrategyRequest 是创建策略的请求体。
type CreateJoinApprovalStrategyRequest struct {
	// GroupOpenIDs 与 GroupIDs 二选一，各最多 100 个群。
	GroupOpenIDs []string `json:"group_openids,omitempty"`
	GroupIDs     GroupIDs `json:"group_ids,omitempty"`
	// IsEnable 空串用平台默认 on。
	IsEnable string `json:"is_enable,omitempty"`
	// ExpireAt 空串用平台默认（一年后过期），非空须 RFC3339。
	ExpireAt string `json:"expire_at,omitempty"`
	Remark   string `json:"remark,omitempty"`
}

// StrategyState 是创建/修改策略的响应（strategy_id 仅创建时返回）。
type StrategyState struct {
	StrategyID string `json:"strategy_id"`
	IsEnable   string `json:"is_enable"`
	ExpireAt   string `json:"expire_at"`
}

// CreateJoinApprovalStrategy 创建策略（POST /v2/groups/join_approval_strategy，60 QPM）。
// 每机器人最多 20 个策略；策略真正生效还要求机器人是群管理员。
func (c *Client) CreateJoinApprovalStrategy(ctx context.Context, req CreateJoinApprovalStrategyRequest) (*StrategyState, error) {
	if err := checkStrategyGroups(req.GroupOpenIDs, req.GroupIDs); err != nil {
		return nil, err
	}
	if err := checkIsEnable(req.IsEnable); err != nil {
		return nil, err
	}
	if err := checkExpireAt(req.ExpireAt); err != nil {
		return nil, err
	}
	if err := checkRemark(req.Remark); err != nil {
		return nil, err
	}
	var out StrategyState
	if err := c.requestJSON(ctx, http.MethodPost, strategyBasePath, req,
		"create join approval strategy", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StrategyGroupAction 是策略关联群的增删（群标识形态须与创建时一致）。
type StrategyGroupAction struct {
	Op           string   `json:"op"` // ApprovalOpAdd / ApprovalOpDel
	GroupOpenIDs []string `json:"group_openids,omitempty"`
	GroupIDs     GroupIDs `json:"group_ids,omitempty"`
}

// UpdateJoinApprovalStrategyRequest 是修改策略的请求体：字段都可选，但至少给一项。
type UpdateJoinApprovalStrategyRequest struct {
	IsEnable    string               `json:"is_enable,omitempty"`
	ExpireAt    string               `json:"expire_at,omitempty"`
	GroupAction *StrategyGroupAction `json:"group_action,omitempty"`
	Remark      string               `json:"remark,omitempty"`
}

// UpdateJoinApprovalStrategy 修改策略的启停、过期时间、备注或关联群
// （PATCH /v2/groups/join_approval_strategy/{strategy_id}，60 QPM）。
func (c *Client) UpdateJoinApprovalStrategy(ctx context.Context, strategyID string, req UpdateJoinApprovalStrategyRequest) (*StrategyState, error) {
	if err := checkStrategyID(strategyID); err != nil {
		return nil, err
	}
	if req.IsEnable == "" && req.ExpireAt == "" && req.Remark == "" && req.GroupAction == nil {
		return nil, fmt.Errorf("api: 修改策略至少给 is_enable/expire_at/group_action/remark 之一")
	}
	if err := checkIsEnable(req.IsEnable); err != nil {
		return nil, err
	}
	if err := checkExpireAt(req.ExpireAt); err != nil {
		return nil, err
	}
	if err := checkRemark(req.Remark); err != nil {
		return nil, err
	}
	if a := req.GroupAction; a != nil {
		if a.Op != ApprovalOpAdd && a.Op != ApprovalOpDel {
			return nil, fmt.Errorf("api: group_action.op 仅支持 add/del，当前 %q", a.Op)
		}
		if err := checkStrategyGroups(a.GroupOpenIDs, a.GroupIDs); err != nil {
			return nil, err
		}
	}
	var out StrategyState
	if err := c.requestJSON(ctx, http.MethodPatch, strategyPath(strategyID, ""), req,
		"update join approval strategy", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteJoinApprovalStrategy 删除策略（DELETE /v2/groups/join_approval_strategy/{strategy_id}，60 QPM）。
func (c *Client) DeleteJoinApprovalStrategy(ctx context.Context, strategyID string) error {
	if err := checkStrategyID(strategyID); err != nil {
		return err
	}
	if err := c.requestJSON(ctx, http.MethodDelete, strategyPath(strategyID, ""), nil,
		"delete join approval strategy", nil); err != nil {
		return err
	}
	return nil
}

// ExecuteJoinApprovalStrategy 对策略关联的全部群发起全量扫描
// （POST /v2/groups/join_approval_strategy/{strategy_id}/execute，60 QPM）。
// 接口异步返回，官方口径约 10 分钟扫描完成。
func (c *Client) ExecuteJoinApprovalStrategy(ctx context.Context, strategyID string) error {
	if err := checkStrategyID(strategyID); err != nil {
		return err
	}
	// 平台按 JSON 解析请求体，显式发 {} 而不是空 body。
	if err := c.requestJSON(ctx, http.MethodPost, strategyPath(strategyID, "/execute"),
		struct{}{}, "execute join approval strategy", nil); err != nil {
		return err
	}
	return nil
}

// JoinApprovalWhitelistRequest 是白名单号码增删的请求体。
type JoinApprovalWhitelistRequest struct {
	Op             string   `json:"op"`              // ApprovalOpAdd / ApprovalOpDel
	WhitelistUsers []string `json:"whitelist_users"` // QQ 号码，十进制字符串
}

// JoinApprovalWhitelistResult 是白名单操作的响应。
type JoinApprovalWhitelistResult struct {
	StrategyID         string `json:"strategy_id"`
	WhitelistUserCount int    `json:"whitelist_user_count"`
	UpdatedAt          string `json:"updated_at"`
}

// UpdateJoinApprovalWhitelist 批量增删白名单号码
// （POST /v2/groups/join_approval_strategy/{strategy_id}/whitelist_users，60 QPM）。
// 单次最多 10000 个；策略白名单总量上限 10 万。
func (c *Client) UpdateJoinApprovalWhitelist(ctx context.Context, strategyID string, req JoinApprovalWhitelistRequest) (*JoinApprovalWhitelistResult, error) {
	if err := checkStrategyID(strategyID); err != nil {
		return nil, err
	}
	if req.Op != ApprovalOpAdd && req.Op != ApprovalOpDel {
		return nil, fmt.Errorf("api: op 仅支持 add/del，当前 %q", req.Op)
	}
	if err := checkWhitelistUsers(req.WhitelistUsers); err != nil {
		return nil, err
	}
	var out JoinApprovalWhitelistResult
	if err := c.requestJSON(ctx, http.MethodPost, strategyPath(strategyID, "/whitelist_users"), req,
		"update join approval whitelist", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateJoinApprovalWhitelistAll 按 10000/次分批提交任意长度的号码列表。
// 逐批串行执行，任一批失败立即返回：已成功的批次不会回滚（错误里带进度）。
func (c *Client) UpdateJoinApprovalWhitelistAll(ctx context.Context, strategyID string, req JoinApprovalWhitelistRequest) (*JoinApprovalWhitelistResult, error) {
	if err := checkStrategyID(strategyID); err != nil {
		return nil, err
	}
	if req.Op != ApprovalOpAdd && req.Op != ApprovalOpDel {
		return nil, fmt.Errorf("api: op 仅支持 add/del，当前 %q", req.Op)
	}
	if len(req.WhitelistUsers) == 0 {
		return nil, fmt.Errorf("api: whitelist_users 不能为空")
	}
	var last *JoinApprovalWhitelistResult
	for start := 0; start < len(req.WhitelistUsers); start += maxWhitelistPerCall {
		end := start + maxWhitelistPerCall
		if end > len(req.WhitelistUsers) {
			end = len(req.WhitelistUsers)
		}
		res, err := c.UpdateJoinApprovalWhitelist(ctx, strategyID, JoinApprovalWhitelistRequest{
			Op:             req.Op,
			WhitelistUsers: req.WhitelistUsers[start:end],
		})
		if err != nil {
			return nil, fmt.Errorf("api: 白名单第 %d~%d 个号码提交失败（前 %d 个已生效）: %w",
				start+1, end, start, err)
		}
		last = res
	}
	return last, nil
}

// checkStrategyID 校验策略 ID（按 ID 操作的接口共用）。
func checkStrategyID(strategyID string) error {
	if strings.TrimSpace(strategyID) == "" {
		return fmt.Errorf("api: strategy_id 不能为空")
	}
	return nil
}

// checkStrategyGroups 校验二选一的群标识：恰好给一种、最多 100 个、openid 无空值。
func checkStrategyGroups(openIDs []string, ids GroupIDs) error {
	switch {
	case len(openIDs) > 0 && len(ids) > 0:
		return fmt.Errorf("api: group_openids 与 group_ids 互斥，只能给一种")
	case len(openIDs) == 0 && len(ids) == 0:
		return fmt.Errorf("api: group_openids 与 group_ids 必须给其中一种")
	}
	if len(openIDs) > maxStrategyGroups || len(ids) > maxStrategyGroups {
		return fmt.Errorf("api: 关联群最多 %d 个（openids=%d, ids=%d）",
			maxStrategyGroups, len(openIDs), len(ids))
	}
	for i, v := range openIDs {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("api: group_openids[%d] 不能为空", i)
		}
	}
	return nil
}

// checkIsEnable 校验 is_enable；空串表示不传（用平台默认）。
func checkIsEnable(v string) error {
	if v == "" || v == StrategyEnableOn || v == StrategyEnableOff {
		return nil
	}
	return fmt.Errorf("api: is_enable 仅支持 on/off，当前 %q", v)
}

// checkExpireAt 校验过期时间；空串表示不传。
func checkExpireAt(v string) error {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return fmt.Errorf("api: expire_at 必须是 RFC3339: %w", err)
	}
	return nil
}

// checkRemark 校验备注长度（按 rune 计，官方 255）。
func checkRemark(v string) error {
	if n := utf8.RuneCountInString(v); n > maxStrategyRemarkRunes {
		return fmt.Errorf("api: remark 最多 %d 个字符（当前 %d）", maxStrategyRemarkRunes, n)
	}
	return nil
}

// checkWhitelistUsers 校验号码列表：1~10000 个、非空、全为十进制数字。
func checkWhitelistUsers(users []string) error {
	if len(users) == 0 {
		return fmt.Errorf("api: whitelist_users 不能为空")
	}
	if len(users) > maxWhitelistPerCall {
		return fmt.Errorf("api: 单次最多 %d 个号码（当前 %d），超长请用 UpdateJoinApprovalWhitelistAll",
			maxWhitelistPerCall, len(users))
	}
	for i, u := range users {
		if !isAllDigits(u) {
			return fmt.Errorf("api: whitelist_users[%d] 必须是纯数字 QQ 号，当前 %q", i, u)
		}
	}
	return nil
}

// isAllDigits 报告 s 是否非空且全为 ASCII 数字。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
