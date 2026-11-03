package api

// group_admin.go 是群管理域的写/查接口（2026-08-10 / 09-03 文档新增）：
//   - 群成员禁言设置（POST restrict_chat_setting，与 group.go 的 GET 成对）
//   - 群成员列表 / 详情、批量移出、黑名单（内邀能力，平台未开通时返回 11253）
//   - 入群申请列表 / 审批
//
// 与 group.go 的分工：group.go 只放"机器人自身视角"的三个只读查询（info/bot_state/
// restrict_chat_setting 的 GET），本文件放管理员视角的群治理接口。两者共用 getJSON/do。

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 群成员角色（member_role）与入群申请来源（apply_source）取值。
const (
	GroupRoleMember = "member"
	GroupRoleAdmin  = "admin"
	GroupRoleOwner  = "owner"

	JoinApplySelf    = "self_apply"
	JoinApplyInvited = "invited"
	JoinOpApprove    = "approve"
	JoinOpDecline    = "decline"
	BlacklistOpAdd   = "add"
	BlacklistOpDel   = "del"
	MuteOpAdd        = "add"
	MuteOpUpdate     = "update"
	MuteOpDel        = "del"
)

// maxMuteDuration 是单次禁言的最长时间（官方：最大 30 天）。
const maxMuteDuration = 30 * 24 * time.Hour

// GroupMember 是群成员（列表项与详情共用）。
type GroupMember struct {
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	// MemberRole 群内角色：member / admin / owner。
	MemberRole  string `json:"member_role"`
	Bot         bool   `json:"bot"`
	JoinedAt    string `json:"joined_at"` // RFC3339
	UnionOpenID string `json:"union_openid"`
}

// GroupMemberList 是分页的群成员列表（每次最多 30 条）。
type GroupMemberList struct {
	Members    []GroupMember `json:"members"`
	NextCursor string        `json:"next_cursor"` // 空串表示末页
}

// ListGroupMembers 分页拉取群成员（GET /v2/groups/{group_openid}/members，60 QPM）。
//
// 平台标注该能力"内邀接入中"：未开通的机器人会收到 11253（无接口权限）。
// cursor 首次传空，后续传上次响应的 NextCursor。
func (c *Client) ListGroupMembers(ctx context.Context, groupOpenID, cursor string) (*GroupMemberList, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out GroupMemberList
	if err := c.getJSON(ctx, groupPath(groupOpenID, "/members"), q, &out); err != nil {
		return nil, fmt.Errorf("api: list group members %s: %w", groupOpenID, err)
	}
	return &out, nil
}

// ListGroupMembersAll 自动翻页拉取全部群成员（护栏 100 页防游标不前进）。
func (c *Client) ListGroupMembersAll(ctx context.Context, groupOpenID string) ([]GroupMember, error) {
	var all []GroupMember
	cursor := ""
	for page := 0; page < 100; page++ {
		res, err := c.ListGroupMembers(ctx, groupOpenID, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Members...)
		if res.NextCursor == "" {
			return all, nil
		}
		if res.NextCursor == cursor {
			return nil, fmt.Errorf("api: list group members: 游标不前进 %q", cursor)
		}
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("api: list group members: 超过最大页数")
}

// GroupMemberInfo 查询单个群成员详情（GET .../members/{member_openid}，30 QPM，内邀）。
func (c *Client) GroupMemberInfo(ctx context.Context, groupOpenID, memberOpenID string) (*GroupMember, error) {
	if groupOpenID == "" || memberOpenID == "" {
		return nil, fmt.Errorf("api: group_openid/member_openid 不能为空")
	}
	var out GroupMember
	path := groupPath(groupOpenID, "/members/"+url.PathEscape(memberOpenID))
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, fmt.Errorf("api: group member info %s: %w", memberOpenID, err)
	}
	return &out, nil
}

// BatchRemoveMembersRequest 是批量移出群成员的请求体（单次 ≤20 人）。
type BatchRemoveMembersRequest struct {
	// MemberOpenIDs 待移出的成员 openid 列表，1~20 个。
	MemberOpenIDs []string `json:"member_openids"`
	// AddToMemberBlacklist 是否同时加入群黑名单（默认 false）。
	AddToMemberBlacklist bool `json:"add_to_member_blacklist,omitempty"`
}

// BatchRemoveMembersResult 是批量移出的响应。
type BatchRemoveMembersResult struct {
	// RemoveMembersResult 成功时为 "success"。
	RemoveMembersResult string `json:"remove_members_result"`
	// FailOpenIDs 加黑名单失败的 openid（移出可能已成功）。
	FailOpenIDs []string `json:"add_to_member_blacklist_fail_openids"`
}

// BatchRemoveMembers 批量移出群成员（POST .../batch_remove_members，30 QPM，需管理员，内邀）。
func (c *Client) BatchRemoveMembers(ctx context.Context, groupOpenID string, req BatchRemoveMembersRequest) (*BatchRemoveMembersResult, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	if len(req.MemberOpenIDs) == 0 || len(req.MemberOpenIDs) > 20 {
		return nil, fmt.Errorf("api: member_openids 数量必须在 1~20（当前 %d）", len(req.MemberOpenIDs))
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+groupPath(groupOpenID, "/batch_remove_members"), tok, req)
	if err != nil {
		return nil, fmt.Errorf("api: batch remove members: %w", err)
	}
	var out BatchRemoveMembersResult
	if err := decodeJSON(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BlacklistUser 是群黑名单成员。
type BlacklistUser struct {
	UnionOpenID  string `json:"union_openid"`
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	BannedAt     string `json:"banned_at"`
	Bot          bool   `json:"bot"`
}

// MemberBlacklist 是分页的群黑名单。
type MemberBlacklist struct {
	Users      []BlacklistUser `json:"users"`
	NextCursor string          `json:"next_cursor"`
}

// ListMemberBlacklist 查询群黑名单（GET .../member_blacklist，30 QPM，内邀）。
// limit 默认 20、最大 100（0 取平台默认）。
func (c *Client) ListMemberBlacklist(ctx context.Context, groupOpenID, cursor string, limit int) (*MemberBlacklist, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	if limit < 0 {
		return nil, fmt.Errorf("api: limit 不能为负数")
	}
	if limit > 100 {
		limit = 100
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out MemberBlacklist
	if err := c.getJSON(ctx, groupPath(groupOpenID, "/member_blacklist"), q, &out); err != nil {
		return nil, fmt.Errorf("api: list member blacklist: %w", err)
	}
	return &out, nil
}

// MemberBlacklistRequest 是加/移黑名单的请求体（单次 ≤20 人）。
type MemberBlacklistRequest struct {
	// Op 操作类型：add / del。
	Op string `json:"op"`
	// MemberOpenIDs 目标成员 openid，1~20 个。
	MemberOpenIDs []string `json:"member_openids"`
}

// MemberBlacklistResult 是黑名单操作的响应（失败的 openid）。
type MemberBlacklistResult struct {
	FailOpenIDs []string `json:"fail_openids"`
}

// UpdateMemberBlacklist 加/移群黑名单（POST .../member_blacklist，60 QPM，内邀）。
// 目标当前在群中时不能拉黑（官方约束）。
func (c *Client) UpdateMemberBlacklist(ctx context.Context, groupOpenID string, req MemberBlacklistRequest) (*MemberBlacklistResult, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	if req.Op != BlacklistOpAdd && req.Op != BlacklistOpDel {
		return nil, fmt.Errorf("api: op 仅支持 add/del，当前 %q", req.Op)
	}
	if len(req.MemberOpenIDs) == 0 || len(req.MemberOpenIDs) > 20 {
		return nil, fmt.Errorf("api: member_openids 数量必须在 1~20（当前 %d）", len(req.MemberOpenIDs))
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+groupPath(groupOpenID, "/member_blacklist"), tok, req)
	if err != nil {
		return nil, fmt.Errorf("api: update member blacklist: %w", err)
	}
	var out MemberBlacklistResult
	if err := decodeJSON(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MemberMuteOp 是一条禁言操作。
type MemberMuteOp struct {
	// Op 操作类型：add（新增禁言）/ update（改到期时间）/ del（解除禁言）。
	Op           string `json:"op"`
	MemberOpenID string `json:"member_openid"`
	// MuteExpireAt 禁言到期时间（RFC3339，UTC+8），add/update 必填，del 不传。
	// 官方限制：最长 30 天，只能操作普通成员（不能禁言管理员/群主）。
	MuteExpireAt string `json:"mute_expire_at,omitempty"`
}

// SetGroupMemberMute 设置/更新/解除群成员禁言（POST /v2/groups/{group_openid}/restrict_chat_setting，
// 60 QPM，需群管理员；单次 ≤20 条，最长 30 天）。
//
// 这是 group.go 中 GroupRestrictSetting（GET）的写入口：读到的 members[].mute_expire_at
// 可直接改写成这里的 MuteExpireAt。
func (c *Client) SetGroupMemberMute(ctx context.Context, groupOpenID string, ops []MemberMuteOp) error {
	if groupOpenID == "" {
		return fmt.Errorf("api: group_openid 不能为空")
	}
	if len(ops) == 0 || len(ops) > 20 {
		return fmt.Errorf("api: members 数量必须在 1~20（当前 %d）", len(ops))
	}
	now := time.Now()
	for i, op := range ops {
		switch op.Op {
		case MuteOpAdd, MuteOpUpdate:
			if op.MemberOpenID == "" {
				return fmt.Errorf("api: members[%d].member_openid 不能为空", i)
			}
			if strings.TrimSpace(op.MuteExpireAt) == "" {
				return fmt.Errorf("api: members[%d] 的 op=%s 必须给 mute_expire_at", i, op.Op)
			}
			expire, err := time.Parse(time.RFC3339, op.MuteExpireAt)
			if err != nil {
				return fmt.Errorf("api: members[%d].mute_expire_at 必须是 RFC3339: %w", i, err)
			}
			if expire.After(now.Add(maxMuteDuration)) {
				return fmt.Errorf("api: members[%d] 禁言到期时间超过平台上限 30 天", i)
			}
		case MuteOpDel:
			if op.MemberOpenID == "" {
				return fmt.Errorf("api: members[%d].member_openid 不能为空", i)
			}
		default:
			return fmt.Errorf("api: members[%d].op 仅支持 add/update/del，当前 %q", i, op.Op)
		}
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"members": ops}
	if _, err := c.do(ctx, http.MethodPost, c.baseURL+groupPath(groupOpenID, "/restrict_chat_setting"), tok, body); err != nil {
		return fmt.Errorf("api: set group member mute: %w", err)
	}
	return nil
}

// VerifyQA 是入群验证问答的一对问答。
type VerifyQA struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// VerifyInfo 是入群申请的验证信息。
type VerifyInfo struct {
	// Method 验证方式（如 qa / none，具体取值以平台下发为准）。
	Method        string     `json:"method"`
	VerifyMessage string     `json:"verify_message"`
	ReviewQAList  []VerifyQA `json:"review_qa_list"`
}

// JoinRequest 是一条入群申请。
type JoinRequest struct {
	// JoinRequestID 审批接口的必填参数。
	JoinRequestID string `json:"join_request_id"`
	RiskTips      string `json:"risk_tips"`
	UnionOpenID   string `json:"union_openid"`
	MemberOpenID  string `json:"member_openid"`
	Username      string `json:"username"`
	ApplyAt       string `json:"apply_at"`
	// ApplySource 申请来源：self_apply（自己申请）/ invited（被邀请）。
	ApplySource string      `json:"apply_source"`
	InvitedBy   string      `json:"invited_by"`
	Bot         bool        `json:"bot"`
	VerifyInfo  *VerifyInfo `json:"verify_info"`
}

// JoinRequestList 是分页的入群申请列表（单页最多 50）。
type JoinRequestList struct {
	List       []JoinRequest `json:"list"`
	NextCursor string        `json:"next_cursor"`
}

// JoinRequestList 拉取入群申请列表（GET .../join_request_list，30 QPM，需管理员）。
// cursor 首次传空；limit 为 0 时用平台默认（20），最大 50。
func (c *Client) JoinRequestList(ctx context.Context, groupOpenID, cursor string, limit int) (*JoinRequestList, error) {
	if groupOpenID == "" {
		return nil, fmt.Errorf("api: group_openid 不能为空")
	}
	if limit < 0 {
		return nil, fmt.Errorf("api: limit 不能为负数")
	}
	if limit > 50 {
		limit = 50
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out JoinRequestList
	if err := c.getJSON(ctx, groupPath(groupOpenID, "/join_request_list"), q, &out); err != nil {
		return nil, fmt.Errorf("api: join request list: %w", err)
	}
	return &out, nil
}

// JoinApprovalRequest 是审批入群申请的请求体。
type JoinApprovalRequest struct {
	// Op 审批动作：approve / decline。
	Op string `json:"op"`
	// JoinRequestID 来自 JoinRequest.JoinRequestID（事件或列表接口）。
	JoinRequestID string `json:"join_request_id"`
	// RejectReason 拒绝理由（op=decline 时可选）。
	RejectReason string `json:"reject_reason,omitempty"`
	// AddToMemberBlacklist 拒绝时是否同时拉黑（op=decline 时可选）。
	AddToMemberBlacklist bool `json:"add_to_member_blacklist,omitempty"`
}

// ApprovalJoinRequest 审批入群申请（POST .../approval_join_request/{member_openid}，
// 60 QPM，需管理员）。
func (c *Client) ApprovalJoinRequest(ctx context.Context, groupOpenID, memberOpenID string, req JoinApprovalRequest) error {
	if groupOpenID == "" || memberOpenID == "" {
		return fmt.Errorf("api: group_openid/member_openid 不能为空")
	}
	if req.Op != JoinOpApprove && req.Op != JoinOpDecline {
		return fmt.Errorf("api: op 仅支持 approve/decline，当前 %q", req.Op)
	}
	if strings.TrimSpace(req.JoinRequestID) == "" {
		return fmt.Errorf("api: join_request_id 不能为空")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	path := groupPath(groupOpenID, "/approval_join_request/"+url.PathEscape(memberOpenID))
	if _, err := c.do(ctx, http.MethodPost, c.baseURL+path, tok, req); err != nil {
		return fmt.Errorf("api: approval join request: %w", err)
	}
	return nil
}

// groupPath 拼 /v2/groups/{openid} + 后缀。
func groupPath(groupOpenID, suffix string) string {
	return "/v2/groups/" + url.PathEscape(groupOpenID) + suffix
}
