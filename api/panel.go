package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// 指令面板 scope（生效场景）取值。
const (
	PanelScopeC2C     = "c2c"     // 单聊
	PanelScopeGroup   = "group"   // 群聊
	PanelScopeChannel = "channel" // 文字子频道
	PanelScopeDM      = "dm"      // 频道私信
)

// 指令面板 target_type（作用范围）取值。
const (
	PanelTargetAll      = "all"      // 对该场景下所有用户/群生效
	PanelTargetSpecific = "specific" // 仅对指定用户/群生效（仅 c2c/group 场景支持）
)

// 指令面板元素 type 取值。
const (
	PanelItemCommand = "command" // 指令：用户点击后内容填入聊天输入框
	PanelItemLink    = "link"    // 链接跳转
)

// 指令面板关联对象操作类型（op）。
const (
	PanelTargetOpAdd = "add"
	PanelTargetOpDel = "del"
)

// PanelItem 是面板中的一个元素（指令或链接）。
type PanelItem struct {
	// Name 元素名称，最多 14 个字符（约 7 个中文汉字）。
	// type=command 时用户点击后该内容会填入聊天输入框；type=link 时仅用于展示。
	Name string `json:"name,omitempty"`
	// Desc 元素描述，最多 30 个字符，在面板中展示给用户。
	Desc string `json:"desc,omitempty"`
	// Type 元素类型：command / link。
	Type string `json:"type,omitempty"`
	// OnlyAdmin 为 true 时仅频道/群管理员可点击。
	OnlyAdmin bool `json:"only_admin,omitempty"`
	// Link 跳转 URL，仅 type=link 时有效（需 https:// 开头）。
	Link string `json:"link,omitempty"`
}

// Panel 是面板配置内容。
type Panel struct {
	// Items 面板元素列表，一个面板最多 20 个。
	Items []PanelItem `json:"items,omitempty"`
	// Remark 面板备注（不对用户展示），最多 255 个字符。
	Remark  string `json:"remark,omitempty"`
	Version int    `json:"version,omitempty"`
}

// PanelRecord 是指令面板记录（列表项与详情共用；关联 openid 仅详情接口返回）。
type PanelRecord struct {
	PanelID    string `json:"panel_id"`
	Scope      string `json:"scope"`
	TargetType string `json:"target_type"`
	Panel      Panel  `json:"panel"`
	CreatedAt  string `json:"created_at"` // RFC3339
	UpdatedAt  string `json:"updated_at"` // RFC3339
	Version    int    `json:"version"`
	// UserOpenIDs 关联用户列表：仅 c2c 且 target_type=specific 时返回，最多 1000 条。
	UserOpenIDs []string `json:"user_openids,omitempty"`
	// GroupOpenIDs 关联群列表：仅 group 且 target_type=specific 时返回，最多 1000 条。
	GroupOpenIDs []string `json:"group_openids,omitempty"`
}

// PanelList 是分页的指令面板列表。
type PanelList struct {
	Records    []PanelRecord `json:"records"`
	NextCursor string        `json:"next_cursor"` // 空串表示已到最后一页
	IsEnd      bool          `json:"is_end"`
}

// PanelCreateRequest 是创建指令面板的请求体。
type PanelCreateRequest struct {
	// Scope 生效场景（必填）：c2c / group / channel / dm。
	Scope string `json:"scope"`
	// TargetType 作用范围：all / specific。channel 与 dm 场景只能为 all。
	TargetType string `json:"target_type,omitempty"`
	// UserOpenIDs 用户列表，仅 c2c 且 specific 时有效，一次最多 20 个。
	UserOpenIDs []string `json:"user_openids,omitempty"`
	// GroupOpenIDs 群列表，仅 group 且 specific 时有效，一次最多 20 个。
	GroupOpenIDs []string `json:"group_openids,omitempty"`
	// Panel 面板配置内容（必填）。
	Panel Panel `json:"panel"`
}

// PanelTargetRequest 是修改面板关联对象的请求体。
type PanelTargetRequest struct {
	// Op 操作类型（必填）：add / del。
	Op string `json:"op"`
	// UserOpenIDs 用户列表，仅 c2c 场景有效，一次最多 20 个。
	UserOpenIDs []string `json:"user_openids,omitempty"`
	// GroupOpenIDs 群列表，仅 group 场景有效，一次最多 20 个。
	GroupOpenIDs []string `json:"group_openids,omitempty"`
}

// ListPanels 分页拉取指定场景下已生效的指令面板列表（按设置时间倒序，30 QPM）。
// scope 必填；cursor 首次传空，后续传上次响应的 NextCursor；limit 默认 20、最大 50（0 表示用默认）。
func (c *Client) ListPanels(ctx context.Context, scope, cursor string, limit int) (*PanelList, error) {
	if scope == "" {
		return nil, fmt.Errorf("api: scope 不能为空")
	}
	q := url.Values{"scope": {scope}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out PanelList
	if err := c.getJSON(ctx, "/v2/panels", q, &out); err != nil {
		return nil, fmt.Errorf("api: list panels: %w", err)
	}
	return &out, nil
}

// ListPanelsAll 拉取指定场景下的全部面板（自动翻页）。一个机器人最多 20 个面板，
// 通常一页拿完；maxPages 护栏只防服务端游标异常导致的死循环。
func (c *Client) ListPanelsAll(ctx context.Context, scope string) ([]PanelRecord, error) {
	var all []PanelRecord
	cursor := ""
	for page := 0; page < 100; page++ {
		p, err := c.ListPanels(ctx, scope, cursor, 50)
		if err != nil {
			return nil, err
		}
		all = append(all, p.Records...)
		if p.IsEnd || p.NextCursor == "" {
			return all, nil
		}
		if p.NextCursor == cursor {
			return nil, fmt.Errorf("api: list panels: 游标不前进 %q", cursor)
		}
		cursor = p.NextCursor
	}
	return nil, fmt.Errorf("api: list panels: 超过最大页数")
}

// CreatePanel 创建指令面板（10 QPM，一个机器人最多 20 个面板），返回新面板 ID。
func (c *Client) CreatePanel(ctx context.Context, req PanelCreateRequest) (string, error) {
	if req.Scope == "" {
		return "", fmt.Errorf("api: scope 不能为空")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return "", err
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+"/v2/panels", tok, req)
	if err != nil {
		return "", fmt.Errorf("api: create panel: %w", err)
	}
	var out struct {
		PanelID string `json:"panel_id"`
	}
	if err := decodeJSON(raw, &out); err != nil {
		return "", err
	}
	if out.PanelID == "" {
		return "", fmt.Errorf("api: create panel: 响应缺少 panel_id")
	}
	return out.PanelID, nil
}

// PanelDetail 查询指定指令面板的完整配置详情（含关联的用户/群 openid 列表，30 QPM）。
func (c *Client) PanelDetail(ctx context.Context, panelID string) (*PanelRecord, error) {
	if panelID == "" {
		return nil, fmt.Errorf("api: panel_id 不能为空")
	}
	var out PanelRecord
	if err := c.getJSON(ctx, "/v2/panels/"+url.PathEscape(panelID), nil, &out); err != nil {
		return nil, fmt.Errorf("api: panel detail %s: %w", panelID, err)
	}
	return &out, nil
}

// UpdatePanel 修改指定面板的配置内容（10 QPM）：覆盖原有元素列表与备注，不影响已关联的用户/群列表。
// 返回修改后的版本号。
func (c *Client) UpdatePanel(ctx context.Context, panelID string, panel Panel) (int, error) {
	if panelID == "" {
		return 0, fmt.Errorf("api: panel_id 不能为空")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return 0, err
	}
	body := map[string]Panel{"panel": panel}
	raw, err := c.do(ctx, http.MethodPut, c.baseURL+"/v2/panels/"+url.PathEscape(panelID), tok, body)
	if err != nil {
		return 0, fmt.Errorf("api: update panel %s: %w", panelID, err)
	}
	var out struct {
		Version int `json:"version"`
	}
	if err := decodeJSON(raw, &out); err != nil {
		return 0, err
	}
	return out.Version, nil
}

// DeletePanel 删除指定指令面板（10 QPM）。删除后该面板不再对任何用户或群生效。
func (c *Client) DeletePanel(ctx context.Context, panelID string) error {
	if panelID == "" {
		return fmt.Errorf("api: panel_id 不能为空")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodDelete, c.baseURL+"/v2/panels/"+url.PathEscape(panelID), tok, nil); err != nil {
		return fmt.Errorf("api: delete panel %s: %w", panelID, err)
	}
	return nil
}

// UpdatePanelTargets 添加/移除面板关联的用户或群（60 QPM）。
// c2c 场景操作用户 openid，group 场景操作群 openid；channel 与 dm 为全局配置，不支持此操作。
func (c *Client) UpdatePanelTargets(ctx context.Context, panelID string, req PanelTargetRequest) error {
	if panelID == "" {
		return fmt.Errorf("api: panel_id 不能为空")
	}
	if req.Op != PanelTargetOpAdd && req.Op != PanelTargetOpDel {
		return fmt.Errorf("api: op 仅支持 add/del，当前 %q", req.Op)
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	path := "/v2/panels/" + url.PathEscape(panelID) + "/target"
	if _, err := c.do(ctx, http.MethodPut, c.baseURL+path, tok, req); err != nil {
		return fmt.Errorf("api: update panel targets %s: %w", panelID, err)
	}
	return nil
}

// decodeJSON 把响应体解码到 out；空体直接视为成功。
func decodeJSON(raw []byte, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("api: decode response: %w", err)
	}
	return nil
}
