package api

import (
	"context"
	"fmt"
	"net/http"
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

// Interact 回应互动事件（仅 type=11/12 必须回应）；同一 interaction_id 只能回应一次。
// interactionID 取事件的 EventID。
func (c *Client) Interact(ctx context.Context, interactionID string, code int) error {
	if interactionID == "" {
		return fmt.Errorf("api: interaction_id 不能为空")
	}
	return c.requestJSON(ctx, http.MethodPut, interactionPath(interactionID), map[string]int{"code": code},
		"interact "+interactionID, nil)
}
