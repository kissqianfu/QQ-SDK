package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/qqtypes"
	"github.com/kissqianfu/QQ-SDK/send"
)

// Target 是发送目标。
type Target struct {
	Scene event.Scene
	ID    string // group_openid 或 user_openid
}

// Result 是发送结果。
type Result struct {
	ID        string
	Timestamp time.Time
	// RefIdx 是平台下发的引用索引（ext_info.ref_idx），
	// 机器人对自己发的消息做引用回复时，把它传给 send.Message.Reference。
	RefIdx string
}

// Send 发送一条消息（群/单聊）；先做本地协议校验，再走 REST。
func (c *Client) Send(ctx context.Context, target Target, msg *send.Message) (*Result, error) {
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	path, err := messagePath(target)
	if err != nil {
		return nil, err
	}
	body, err := buildSendBody(msg)
	if err != nil {
		return nil, err
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodPost, c.baseURL+path, tok, body)
	if err != nil {
		return nil, err
	}
	return parseSendResult(raw)
}

// Me 拉取机器人自身信息（GET /users/@me）；启动时调用，失败应拒绝启动。
func (c *Client) Me(ctx context.Context) (*qqtypes.User, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodGet, c.baseURL+"/users/@me", tok, nil)
	if err != nil {
		return nil, err
	}
	var user qqtypes.User
	if err := json.Unmarshal(raw, &user); err != nil {
		return nil, fmt.Errorf("api: decode me: %w", err)
	}
	return &user, nil
}

// scenePath 拼场景相关路径（发消息与上传共用）：/v2/groups/{id} 或 /v2/users/{id} + 后缀。
func scenePath(scene event.Scene, targetID, suffix string) (string, error) {
	if targetID == "" {
		return "", fmt.Errorf("api: 目标 id 不能为空")
	}
	id := url.PathEscape(targetID)
	switch scene {
	case event.SceneGroup:
		return "/v2/groups/" + id + suffix, nil
	case event.SceneC2C:
		return "/v2/users/" + id + suffix, nil
	default:
		return "", fmt.Errorf("api: 未知场景 %d", scene)
	}
}

// messagePath 按场景拼接发消息路径。
func messagePath(target Target) (string, error) {
	return scenePath(target.Scene, target.ID, "/messages")
}

// buildSendBody 把 send.Message 变成平台请求体。
func buildSendBody(msg *send.Message) (map[string]any, error) {
	body := map[string]any{"msg_type": msgType(msg.Kind())}
	switch msg.Kind() {
	case send.KindText:
		body["content"] = msg.Content()
	case send.KindMarkdown:
		if tpl := msg.MarkdownTemplateData(); tpl != nil {
			params := make([]map[string]any, 0, len(tpl.Params))
			for _, p := range tpl.Params {
				params = append(params, map[string]any{"key": p.Key, "values": p.Values})
			}
			// 官方 custom_template_id 是 string 类型（当前文档标记已废弃，但无模板权限的 bot 仍需走模板）。
			body["markdown"] = map[string]any{"custom_template_id": tpl.ID, "params": params}
		} else {
			body["markdown"] = map[string]string{"content": msg.Content()}
		}
	case send.KindMedia:
		body["media"] = map[string]string{"file_info": msg.MediaFileInfo()}
		// 官方要求 msg_type=7 时 content 有值（可为空格），否则部分场景会被平台拒绝。
		body["content"] = " "
	case send.KindCard:
		// msg_type=8 时必须传独立 card 字段（官方 MessageCard：type=tuwen + content）。
		body["card"] = cardPayload(msg.CardData())
	}

	// 被动回复锚点：msg_id 与 event_id 二选一（Validate 已保证）。
	if msgID, seq := msg.Reply(); msgID != "" {
		body["msg_id"] = msgID
		body["msg_seq"] = seq
	} else if evID, seq := msg.ReplyEvent(); evID != "" {
		body["event_id"] = evID
		body["msg_seq"] = seq
	} else if seq := msg.SeqNo(); seq > 0 {
		// 主动消息也带 msg_seq：避免平台按 seq + 内容把重复内容判重。
		body["msg_seq"] = seq
	}
	if ref := msg.ReferenceIdx(); ref != "" {
		body["message_reference"] = map[string]any{"message_id": ref, "ignore_get_error": true}
	}
	if kb := msg.KeyboardData(); kb != nil {
		body["keyboard"] = keyboardBody(*kb)
	} else if kbID := msg.KeyboardTemplateID(); kbID != "" {
		// 短形式：只传模板 id。
		body["keyboard"] = map[string]string{"id": kbID}
	}
	return body, nil
}

// msgType 把语义类型映射成协议数字（数字只出现在 SDK 内部，业务不接触）。
func msgType(kind send.Kind) int {
	switch kind {
	case send.KindMarkdown:
		return 2
	case send.KindMedia:
		return 7
	case send.KindCard:
		return 8
	default:
		return 0
	}
}

// cardPayload 是图文卡片的协议结构（无图床时不可用）。
func cardPayload(c send.Card) map[string]any {
	return map[string]any{
		"type": "tuwen",
		"content": map[string]string{
			"title":       c.Title,
			"description": c.Description,
			"url":         c.URL,
			"pic_url":     c.PicURL,
		},
	}
}

// keyboardBody 把键盘转成协议结构。
func keyboardBody(kb send.Keyboard) map[string]any {
	rows := make([]map[string]any, 0, len(kb.Rows))
	for _, row := range kb.Rows {
		buttons := make([]map[string]any, 0, len(row))
		for _, btn := range row {
			// 官方 permission 语义：0=指定用户（配 specify_user_ids），1=管理员，2=所有人。
			permission := map[string]any{"type": 2}
			if len(btn.SpecifyUserIDs) > 0 {
				permission["type"] = 0
				permission["specify_user_ids"] = btn.SpecifyUserIDs
			}
			action := map[string]any{
				"type":           btn.ActionType,
				"permission":     permission,
				"data":           buttonData(btn),
				"unsupport_tips": "当前版本不支持该操作",
			}
			if btn.GroupID != "" && btn.ActionType == 1 {
				action["group_id"] = btn.GroupID
			}
			if btn.ActionType == 2 {
				if btn.Enter {
					action["enter"] = true
				}
				if btn.Reply {
					action["reply"] = true
				}
			}
			if btn.Modal != nil {
				modal := map[string]any{"content": btn.Modal.Content}
				if btn.Modal.ConfirmText != "" {
					modal["confirm_text"] = btn.Modal.ConfirmText
				}
				if btn.Modal.CancelText != "" {
					modal["cancel_text"] = btn.Modal.CancelText
				}
				action["modal"] = modal
			}
			buttons = append(buttons, map[string]any{
				"id": btn.ID,
				"render_data": map[string]any{
					"label":         btn.Label,
					"visited_label": btn.VisitedLabel,
					"style":         btn.Style,
				},
				"action": action,
			})
		}
		rows = append(rows, map[string]any{"buttons": buttons})
	}
	return map[string]any{"content": map[string]any{"rows": rows}}
}

// buttonData 按动作类型取数据：0=链接，1=回调，2=指令。
func buttonData(btn send.Button) string {
	if btn.ActionType == 0 {
		return btn.Link
	}
	return btn.ActionData
}

// parseSendResult 解析发消息响应：timestamp 兼容毫秒数字（旧文档）与 RFC3339 字符串（现文档），
// 归一化收敛在 qqtypes.ParseTime（与 event 共用一份）。
func parseSendResult(raw []byte) (*Result, error) {
	var resp struct {
		ID        string          `json:"id"`
		Timestamp json.RawMessage `json:"timestamp"`
		ExtInfo   struct {
			RefIdx string `json:"ref_idx"`
		} `json:"ext_info"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("api: decode send result: %w", err)
	}
	return &Result{ID: resp.ID, RefIdx: resp.ExtInfo.RefIdx, Timestamp: qqtypes.ParseTime(resp.Timestamp)}, nil
}
