package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// 互动事件响应结果码（PUT /interactions/{interaction_id} 的 code 字段）。
const (
	// InteractOK 表示成功（type=14 清空会话时客户端会下发"已清空"小灰条）。
	InteractOK = 0
	// InteractFail 操作失败。
	InteractFail = 1
	// InteractTooFrequent 操作频繁。
	InteractTooFrequent = 2
	// InteractDuplicate 重复操作。
	InteractDuplicate = 3
	// InteractNoPermission 没有权限。
	InteractNoPermission = 4
	// InteractAdminOnly 仅管理员可操作。
	InteractAdminOnly = 5
)

// Interact 回应互动事件（官方协议：收到 INTERACTION_CREATE 后调用）。
//
// 仅 type=11（消息按钮）与 type=12（快捷菜单）必须回应，否则客户端一直 loading 直到超时；
// 其他类型回应也不会报错。同一 interaction_id 只能回应一次，超时后失效。
// interactionID 取 INTERACTION_CREATE 事件的 id（SDK 归一化后是 Event.EventID）。
func (c *Client) Interact(ctx context.Context, interactionID string, code int) error {
	if interactionID == "" {
		return fmt.Errorf("api: interaction_id 不能为空")
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	path := "/interactions/" + url.PathEscape(interactionID)
	if _, err := c.do(ctx, http.MethodPut, c.baseURL+path, tok, map[string]int{"code": code}); err != nil {
		return fmt.Errorf("api: interact %s: %w", interactionID, err)
	}
	return nil
}
