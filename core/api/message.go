package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/kissqianfu/QQ-SDK/common/qqtypes"
	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/send"
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
	// RemainMsgLen 是流式消息的剩余可写字符数（仅流式响应下发；nil = 平台未给）。
	RemainMsgLen *int
}

// Send 发送一条消息（群/单聊）；先做本地协议校验，再走 REST。
func (c *Client) Send(ctx context.Context, target Target, msg *send.Message) (*Result, error) {
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	if err := c.checkContentLen(msg); err != nil {
		return nil, err
	}
	path, err := messagePath(target)
	if err != nil {
		return nil, err
	}
	body := buildSendBody(msg)
	// 主动消息过本地三层配额（本地拒绝时不触达平台）。
	active := isActiveMessage(msg)
	if active {
		if err := c.quota.acquire(target.Scene, target.ID); err != nil {
			return nil, err
		}
	}
	// delivered 标记"平台已接受"：未送达（Token 失败/网络/5xx/429/4xx）时退令牌。
	delivered := false
	if active {
		defer func() {
			if !delivered {
				c.quota.refund(target.Scene, target.ID)
			}
		}()
	}
	raw, err := c.doPath(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	// 2xx 即平台已受理（即便响应体解析失败也不退，见 TestQuotaNotRefundedAfterPlatformAccepted）。
	delivered = true
	res, err := parseSendResult(raw)
	if err != nil {
		return nil, err
	}
	// 日上限只统计真正发出去的主动消息（失败不占额度）。
	if active {
		c.quota.noteSent(target.Scene, target.ID)
	}
	if res.ID == "" {
		// 没有 message_id 时显式失败。
		return nil, fmt.Errorf("api: send 响应缺少 message_id")
	}
	return res, nil
}

// checkContentLen 在本地拦下超长正文（Config.MaxContentLen，0 = 不校验）。
func (c *Client) checkContentLen(msg *send.Message) error {
	if c.maxContentLen <= 0 {
		return nil
	}
	switch msg.Kind() {
	case send.KindText, send.KindMarkdown:
	default:
		return nil
	}
	if n := utf8.RuneCountInString(msg.Content()); n > c.maxContentLen {
		return fmt.Errorf("api: 正文 %d 字符超过本地上限 %d（Config.MaxContentLen）", n, c.maxContentLen)
	}
	return nil
}

// isActiveMessage 判断是否为主动消息：既没有 msg_id 也没有 event_id 锚点（须与平台口径一致）。
func isActiveMessage(msg *send.Message) bool {
	if id, _ := msg.Reply(); id != "" {
		return false
	}
	if id, _ := msg.ReplyEvent(); id != "" {
		return false
	}
	return true
}

// Recall 撤回机器人自己发送的消息（发送后 2 分钟内有效；msgID 取 Send/StartStream 响应的 Result.ID）。
func (c *Client) Recall(ctx context.Context, target Target, msgID string) error {
	if msgID == "" {
		return fmt.Errorf("api: 撤回的 message_id 不能为空")
	}
	path, err := sceneMessagePath(target.Scene, target.ID, msgID)
	if err != nil {
		return err
	}
	return c.requestJSON(ctx, http.MethodDelete, path, nil, "recall message "+msgID, nil)
}

// InputNotify 发送"正在输入"状态（仅单聊）；必须带被动回复锚点 msgID。
// seconds 取值 1~60，0 表示按平台默认 60 秒；msgSeq 为 0 时按 1 处理。
func (c *Client) InputNotify(ctx context.Context, userOpenID, msgID string, msgSeq, seconds int) error {
	if userOpenID == "" {
		return fmt.Errorf("api: user_openid 不能为空")
	}
	if msgID == "" {
		return fmt.Errorf("api: input_notify 必须带被动回复 msg_id")
	}
	if seconds <= 0 {
		seconds = 60
	}
	if seconds > 60 {
		return fmt.Errorf("api: input_second 最长 60 秒（当前 %d）", seconds)
	}
	if msgSeq <= 0 {
		msgSeq = 1
	}
	body := map[string]any{
		"msg_type": 6,
		"input_notify": map[string]any{
			"input_type":   1,
			"input_second": seconds,
		},
		"msg_id":  msgID,
		"msg_seq": msgSeq,
	}
	path, err := scenePath(event.SceneC2C, userOpenID, "/messages")
	if err != nil {
		return err
	}
	return c.requestJSON(ctx, http.MethodPost, path, body, "input notify", nil)
}

// Me 拉取机器人自身信息（GET /users/@me）；启动时调用，失败应拒绝启动。
func (c *Client) Me(ctx context.Context) (*qqtypes.User, error) {
	var user qqtypes.User
	if err := c.requestJSON(ctx, http.MethodGet, mePath, nil, "get me", &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// buildSendBody 把 send.Message 变成平台请求体（合法性已由 send.Message.Validate 保证）。
func buildSendBody(msg *send.Message) map[string]any {
	body := map[string]any{"msg_type": msgType(msg.Kind())}
	switch msg.Kind() {
	case send.KindText:
		body["content"] = msg.Content()
	case send.KindMarkdown:
		md := map[string]any{}
		if tpl := msg.MarkdownTemplateData(); tpl != nil {
			params := make([]map[string]any, 0, len(tpl.Params))
			for _, p := range tpl.Params {
				params = append(params, map[string]any{"key": p.Key, "values": p.Values})
			}
			// 官方 custom_template_id 是 string 类型（当前文档标记已废弃，但无模板权限的 bot 仍需走模板）。
			md["custom_template_id"] = tpl.ID
			md["params"] = params
		} else {
			md["content"] = msg.Content()
		}
		// 官方 force_verify_image_resource：图片转存失败时中断发送并报错（默认关闭）。
		if msg.ImageVerifyEnabled() {
			md["force_verify_image_resource"] = true
		}
		body["markdown"] = md
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
		// 主动消息也带 msg_seq。
		body["msg_seq"] = seq
	}
	// 互动召回消息：官方 is_wakeup 与 msg_id/event_id 互斥（Validate 已保证）。
	if msg.WakeupEnabled() {
		body["is_wakeup"] = true
	}
	if ref := msg.ReferenceIdx(); ref != "" {
		// 按官方文档收敛为单字段 message_id。
		body["message_reference"] = map[string]any{"message_id": ref}
	}
	if kb := msg.KeyboardData(); kb != nil {
		body["keyboard"] = keyboardBody(*kb)
	} else if kbID := msg.KeyboardTemplateID(); kbID != "" {
		// 短形式：只传模板 id。
		body["keyboard"] = map[string]string{"id": kbID}
	}
	return body
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
			// permission.type 的推断规则由 send 包定义（Validate 与序列化共用一份）。
			permType := btn.PermissionTypeFor()
			permission := map[string]any{"type": permType}
			if permType == 0 {
				if len(btn.SpecifyUserIDs) > 0 {
					permission["specify_user_ids"] = btn.SpecifyUserIDs
				}
				if len(btn.SpecifyRoleIDs) > 0 {
					permission["specify_role_ids"] = btn.SpecifyRoleIDs
				}
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
				// 官方 anchor：置 1 时点击按钮自动唤起手Q选图器（设置后忽略 enter）。
				if btn.Anchor != 0 {
					action["anchor"] = btn.Anchor
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

// sendResultData 是发消息响应的解析模型。
type sendResultData struct {
	ID           string          `json:"id"`
	Timestamp    json.RawMessage `json:"timestamp"`
	RemainMsgLen *int            `json:"remain_msg_len"`
	ExtInfo      struct {
		RefIdx string `json:"ref_idx"`
	} `json:"ext_info"`
}

func parseSendResult(raw []byte) (*Result, error) {
	var resp sendResultData
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("api: decode send result: %w", err)
	}
	return &Result{
		ID:           resp.ID,
		RefIdx:       resp.ExtInfo.RefIdx,
		Timestamp:    qqtypes.ParseTime(resp.Timestamp),
		RemainMsgLen: resp.RemainMsgLen,
	}, nil
}
