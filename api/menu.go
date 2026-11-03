package api

// menu.go 是"全局自定义菜单"域（2026-08-12 文档新增）。
//
// 与 panel.go 的区别：
//   - 菜单（/v2/menu）是**单聊**输入框旁的固定入口，全局生效、对所有用户一致，只有一套；
//   - 指令面板（/v2/panels）是 "/" 拉起的命令面板，可按 c2c/group/channel/dm 场景与
//     指定用户/群定向生效，可多套。
//
// 菜单 PUT 是**全量覆盖**语义：传空 items 等于清空菜单。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// 菜单项类型（name 长度按"ASCII 算 1、非 ASCII 算 2"计数）。
const (
	MenuItemSwitch      = "switch"       // 开关
	MenuItemSendMessage = "send_message" // 点击后把文本填入输入框
	MenuItemLink        = "link"         // 跳转链接
	MenuItemMenu        = "menu"         // 折叠子菜单
)

const (
	// maxMenuItems 是一级菜单项上限。
	maxMenuItems = 10
	// maxMenuWidth 是一级菜单项名称宽度上限（汉字算 2）。
	maxMenuWidth = 10
	// maxSubMenuItems 是子菜单项上限。
	maxSubMenuItems = 5
	// maxSubMenuWidth 是子菜单项名称宽度上限（汉字算 2）。
	maxSubMenuWidth = 14
)

// MenuSwitch 是 switch 类型菜单项的开关配置。
type MenuSwitch struct {
	// SwitchID 开关唯一标识：用户切换后，平台发来的消息 content/ext 里会带 "switchID=1"。
	SwitchID string `json:"switch_id"`
	// Default 初始状态：true=打开。
	Default bool `json:"default"`
}

// MenuSubItem 是子菜单项（type 只能是 send_message / link）。
type MenuSubItem struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	SendMessage string `json:"send_message,omitempty"`
	Link        string `json:"link,omitempty"`
}

// MenuItem 是一级菜单项。
type MenuItem struct {
	Name string `json:"name"`
	// Type 取 MenuItemSwitch / MenuItemSendMessage / MenuItemLink / MenuItemMenu。
	Type string `json:"type"`
	// SubMenuItems 仅 type=menu 时有效，最多 5 个，不支持再嵌套。
	SubMenuItems []MenuSubItem `json:"sub_menu_items,omitempty"`
	// SendMessage 仅 type=send_message 时有效。
	SendMessage string `json:"send_message,omitempty"`
	// Link 仅 type=link 时有效，必须 https:// 开头。
	Link string `json:"link,omitempty"`
	// Switch 仅 type=switch 时有效。
	Switch *MenuSwitch `json:"switch,omitempty"`
}

// Menu 是菜单配置；PATCH 语义为全量覆盖。
type Menu struct {
	// Items 菜单项列表，最多 10 个，从左到右展示。
	Items []MenuItem `json:"items,omitempty"`
}

// MenuState 是 GET /v2/menu 的响应（菜单 + 版本号）。
type MenuState struct {
	Menu    Menu `json:"menu"`
	Version int  `json:"version"`
}

// GetMenu 查询全局自定义菜单（GET /v2/menu，30 QPM）。未配置过时菜单为空、version 为 0。
func (c *Client) GetMenu(ctx context.Context) (*MenuState, error) {
	var out MenuState
	if err := c.getJSON(ctx, "/v2/menu", nil, &out); err != nil {
		return nil, fmt.Errorf("api: get menu: %w", err)
	}
	return &out, nil
}

// PutMenu 全量覆盖全局自定义菜单（PUT /v2/menu，5 QPM），返回新版本号。
// 传 Menu{Items: nil} 等于清空菜单。
func (c *Client) PutMenu(ctx context.Context, menu Menu) (int, error) {
	if err := validateMenu(menu); err != nil {
		return 0, err
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return 0, err
	}
	raw, err := c.do(ctx, http.MethodPut, c.baseURL+"/v2/menu", tok, map[string]Menu{"menu": menu})
	if err != nil {
		return 0, fmt.Errorf("api: put menu: %w", err)
	}
	var out struct {
		Version int `json:"version"`
	}
	if err := decodeJSON(raw, &out); err != nil {
		return 0, err
	}
	return out.Version, nil
}

// DeleteMenu 清空全局菜单（PUT 空 items 的便捷封装）。
func (c *Client) DeleteMenu(ctx context.Context) (int, error) {
	return c.PutMenu(ctx, Menu{})
}

// menuWidth 按官方口径计宽：ASCII 记 1，非 ASCII（汉字/全角）记 2。
func menuWidth(s string) int {
	n := 0
	for _, r := range s {
		if r > 0x7f {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// validateMenu 做发送前的本地校验（全部来自官方请求体的硬约束）。
func validateMenu(menu Menu) error {
	if len(menu.Items) > maxMenuItems {
		return fmt.Errorf("api: 菜单项最多 %d 个（当前 %d）", maxMenuItems, len(menu.Items))
	}
	for i, item := range menu.Items {
		if w := menuWidth(item.Name); w < 1 || w > maxMenuWidth {
			return fmt.Errorf("api: items[%d].name 宽度必须在 1~%d（汉字算 2，当前 %d）", i, maxMenuWidth, w)
		}
		switch item.Type {
		case MenuItemSwitch:
			if item.Switch == nil || strings.TrimSpace(item.Switch.SwitchID) == "" {
				return fmt.Errorf("api: items[%d] 是 switch 类型但缺少 switch.switch_id", i)
			}
		case MenuItemSendMessage:
			if strings.TrimSpace(item.SendMessage) == "" {
				return fmt.Errorf("api: items[%d] 是 send_message 类型但 send_message 为空", i)
			}
		case MenuItemLink:
			if err := checkMenuHTTPS(fmt.Sprintf("items[%d].link", i), item.Link); err != nil {
				return err
			}
		case MenuItemMenu:
			if len(item.SubMenuItems) == 0 || len(item.SubMenuItems) > maxSubMenuItems {
				return fmt.Errorf("api: items[%d] 子菜单数量必须在 1~%d（当前 %d）",
					i, maxSubMenuItems, len(item.SubMenuItems))
			}
			for j, sub := range item.SubMenuItems {
				if w := menuWidth(sub.Name); w < 1 || w > maxSubMenuWidth {
					return fmt.Errorf("api: items[%d].sub_menu_items[%d].name 宽度必须在 1~%d（当前 %d）",
						i, j, maxSubMenuWidth, w)
				}
				switch sub.Type {
				case MenuItemSendMessage:
					if strings.TrimSpace(sub.SendMessage) == "" {
						return fmt.Errorf("api: items[%d].sub_menu_items[%d] 是 send_message 但内容为空", i, j)
					}
				case MenuItemLink:
					if err := checkMenuHTTPS(fmt.Sprintf("items[%d].sub_menu_items[%d].link", i, j), sub.Link); err != nil {
						return err
					}
				case MenuItemMenu:
					return fmt.Errorf("api: items[%d].sub_menu_items[%d] 不支持 menu 类型（二级菜单不可再嵌套）", i, j)
				default:
					return fmt.Errorf("api: items[%d].sub_menu_items[%d].type 非法 %q（仅 send_message/link）", i, j, sub.Type)
				}
			}
		default:
			return fmt.Errorf("api: items[%d].type 非法 %q（仅 switch/send_message/link/menu）", i, item.Type)
		}
	}
	return nil
}

// checkMenuHTTPS 校验菜单链接（官方：必须 https:// 开头）。
func checkMenuHTTPS(field, raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("api: 菜单 %s 必须以 https:// 开头（当前 %q）", field, raw)
	}
	return nil
}
