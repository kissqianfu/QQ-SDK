// Package send 构造协议正确的出站消息。
//
// 为什么 Card/Keyboard 的校验放在 SDK：title≤50、url 必须 https、字段互斥是**协议约束**，
// 不是业务逻辑，放在这里任何人用都不会踩。
package send

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// 平台协议上限（超过会被平台拒绝）。
const (
	maxTitleRunes = 50
	maxDescRunes  = 200
	maxURLLen     = 1024
	maxRows       = 5
	maxButtons    = 5
	maxLabelRunes = 10

	// AtEveryone 是群聊 @全体成员 的占位符（2026-10-26 文档新增）：
	// 文本/Markdown 里写它即可 @ 全部成员。要求机器人是群管理员，且每个群每天只能用 1 次
	// （超限平台返回 304019）。
	AtEveryone = "<qqbot-at-everyone />"
)

// Kind 是消息内容类型（协议 msg_type 的语义化名字）。
type Kind uint8

const (
	// KindText 是纯文本。
	KindText Kind = iota + 1
	// KindMarkdown 是原生 Markdown。
	KindMarkdown
	// KindCard 是图文卡片（无图床时业务不应产出）。
	KindCard
	// KindMedia 是富媒体。
	KindMedia
)

// Card 是图文卡片内容（title/desc/url/pic_url 都是协议硬约束）。
type Card struct {
	Title       string
	Description string
	URL         string
	PicURL      string
}

// MarkdownParam 是 Markdown 模板参数（模板需在机器人后台审核通过）。
type MarkdownParam struct {
	Key    string
	Values []string
}

// Button 是内嵌键盘按钮。
type Button struct {
	ID             string
	Label          string   // 最多 10 字符（协议约束，Validate 校验）
	VisitedLabel   string   // 点击后文字，不传保持不变
	Style          int      // 0 灰色线框 | 1 蓝色线框 | 3 白底红字 | 4 蓝底白字
	ActionType     int      // 0 跳转链接 | 1 回调 | 2 指令
	ActionData     string   // 回调数据或指令内容
	Link           string   // ActionType=0 时必填
	SpecifyUserIDs []string // 可选：仅指定用户可点（permission type=0 + specify_user_ids）
	// GroupID 是分组 ID：同一分组内点了一个按钮后其余变灰（仅 ActionType=1 回调按钮有效）。
	GroupID string
	// Enter 仅指令按钮（ActionType=2）有效：点击后直接自动发送 data（仅单聊，官方 enter）。
	Enter bool
	// Reply 仅指令按钮有效：指令是否带引用回复本消息（官方 reply）。
	Reply bool
	// Modal 二次确认（官方 modal）：点击按钮先弹确认框。
	Modal *Modal
	// Anchor 仅指令按钮（ActionType=2）有效：置 1 时点击按钮自动唤起手机 QQ 选图器，
	// 设置后会忽略 Enter（官方 anchor，仅手机端 8983+ 单聊场景）。
	Anchor int
	// SpecifyRoleIDs 是权限身份组列表（官方 permission.specify_role_ids，仅频道可用）。
	// 群/单聊场景请用 SpecifyUserIDs；两者可同时存在。
	SpecifyRoleIDs []string
	// PermissionType 覆盖按钮的 permission.type：
	// 1=管理员，2=所有人，3=指定身份组（仅频道）。
	// 零值 0 表示"自动"：有 SpecifyUserIDs/SpecifyRoleIDs 时为 0（指定对象），否则 2（所有人）。
	PermissionType int
}

// Modal 是按钮点击的二次确认框（官方约束：content ≤40 字符且不能有 URL；
// confirm_text/cancel_text ≤4 字符，留空用平台默认"确认"/"取消"）。
type Modal struct {
	Content     string
	ConfirmText string
	CancelText  string
}

// 按钮渲染样式（协议取值）。
const (
	// StyleGray 是灰色线框（默认）。
	StyleGray = 0
	// StyleBlue 是蓝色线框。
	StyleBlue = 1
	// StyleWhiteRed 是白色背景红色字体。
	StyleWhiteRed = 3
	// StyleBlueWhite 是蓝色背景白色字体。
	StyleBlueWhite = 4
)

// Keyboard 是内嵌键盘。
type Keyboard struct{ Rows [][]Button }

// Message 是构造好的消息；字段私有，业务不能拼错协议。
type Message struct {
	kind     Kind
	text     string
	card     Card
	media    string
	keyboard *Keyboard
	kbID     string // keyboard 模板 id（短形式，与 keyboard 互斥）
	mdTpl    *MarkdownTemplate
	refIdx   string
	replyID  string
	replyEv  string // event_id 锚点（与 replyID 互斥）
	seq      int
	// wakeup 是互动召回消息：平台不校验被动回复窗口，但与 msg_id/event_id 互斥。
	wakeup bool
	// forceVerifyImage 对应 markdown.force_verify_image_resource：图片转存失败即报错。
	forceVerifyImage bool
}

// MarkdownTemplate 是 Markdown 模板（custom_template_id + params；官方 custom_template_id 为 string）。
type MarkdownTemplate struct {
	ID     string
	Params []MarkdownParam
}

// Text 构造纯文本消息。
func Text(s string) *Message { return &Message{kind: KindText, text: s} }

// Markdown 构造原生 Markdown 消息。
func Markdown(content string) *Message { return &Message{kind: KindMarkdown, text: content} }

// NewMarkdownTemplate 构造模板 Markdown 消息（custom_template_id + params）。
//
// Deprecated: 官方消息接口已把 MessageMarkdown.template_id / custom_template_id 标为
// 【已废弃】，2026-04-23 起单聊/群聊自定义 Markdown 全量开放、无需模板审核。
// 新代码请直接用 send.Markdown(content)；本函数仅为存量模板场景保留。
func NewMarkdownTemplate(templateID string, params ...MarkdownParam) *Message {
	return &Message{kind: KindMarkdown, mdTpl: &MarkdownTemplate{ID: templateID, Params: params}}
}

// NewCard 构造图文卡片消息。
func NewCard(c Card) *Message { return &Message{kind: KindCard, card: c} }

// Media 构造富媒体消息（fileInfo 由上传接口返回）。
func Media(fileInfo string) *Message { return &Message{kind: KindMedia, media: fileInfo} }

// Keyboard 挂自定义内嵌键盘（与 KeyboardTemplate 互斥）。
func (m *Message) Keyboard(k Keyboard) *Message {
	m.keyboard = &k
	return m
}

// KeyboardTemplate 挂模板键盘（短形式只传模板 id；与 Keyboard 互斥）。
func (m *Message) KeyboardTemplate(id string) *Message {
	m.kbID = id
	return m
}

// Reference 引用回复（refIdx 来自入站消息）。
func (m *Message) Reference(refIdx string) *Message {
	m.refIdx = refIdx
	return m
}

// ReplyTo 设置被动回复锚点：msgID + 单调递增的 msg_seq（平台靠它去重）。
func (m *Message) ReplyTo(msgID string, seq int) *Message {
	m.replyID = msgID
	m.seq = seq
	return m
}

// ReplyToEvent 设置事件锚点被动回复：eventID 取事件最外层 id（SDK 归一化后是 Event.ID）。
//
// 与 ReplyTo 二选一，支持 INTERACTION_CREATE / GROUP_ADD_ROBOT / GROUP_MSG_RECEIVE 场景。
func (m *Message) ReplyToEvent(eventID string, seq int) *Message {
	m.replyEv = eventID
	m.seq = seq
	return m
}

// Seq 设置主动消息的 msg_seq（不设置被动回复锚点）。
//
// 平台按 msg_id + msg_seq 去重；主动消息没有 msg_id，但相同内容连续发送时仍建议
// 使用递增 seq，避免平台按 seq + 内容判定重复。被动回复请用 ReplyTo/ReplyToEvent。
func (m *Message) Seq(seq int) *Message {
	m.seq = seq
	return m
}

// SeqNo 返回当前 msg_seq（0 表示未设置）。
func (m *Message) SeqNo() int { return m.seq }

// IsWakeup 声明本条为互动召回消息（官方 is_wakeup）：
// 用户主动对话后 30 天内、每个周期（当天/1-3 天/3-7 天/7-30 天）可下发 1 条。
// 与 ReplyTo/ReplyToEvent 互斥（Validate 会拒绝同时设置）。
func (m *Message) IsWakeup(v bool) *Message {
	m.wakeup = v
	return m
}

// WakeupEnabled 返回是否声明为互动召回消息。
func (m *Message) WakeupEnabled() bool { return m.wakeup }

// ForceVerifyImageResource 打开 markdown 图片转存强校验（官方 force_verify_image_resource）：
// 图片转存失败时中断发送并返回错误，而不是静默丢弃图片。仅 Markdown 消息有效。
func (m *Message) ForceVerifyImageResource(v bool) *Message {
	m.forceVerifyImage = v
	return m
}

// ImageVerifyEnabled 返回是否打开图片转存强校验。
func (m *Message) ImageVerifyEnabled() bool { return m.forceVerifyImage }

// Kind 返回内容类型。
func (m *Message) Kind() Kind { return m.kind }

// Content 返回文本/Markdown 内容。
func (m *Message) Content() string { return m.text }

// CardData 返回卡片内容。
func (m *Message) CardData() Card { return m.card }

// MediaFileInfo 返回富媒体 file_info。
func (m *Message) MediaFileInfo() string { return m.media }

// KeyboardData 返回自定义键盘（未设置时 nil）。
func (m *Message) KeyboardData() *Keyboard { return m.keyboard }

// KeyboardTemplateID 返回模板键盘 id（未设置时空串）。
func (m *Message) KeyboardTemplateID() string { return m.kbID }

// MarkdownTemplateData 返回模板 Markdown（未设置时 nil；Content 与模板二选一）。
func (m *Message) MarkdownTemplateData() *MarkdownTemplate { return m.mdTpl }

// ReferenceIdx 返回引用索引。
func (m *Message) ReferenceIdx() string { return m.refIdx }

// Reply 返回 msg_id 被动回复锚点（msgID 与 seq）。
func (m *Message) Reply() (msgID string, seq int) { return m.replyID, m.seq }

// ReplyEvent 返回 event_id 被动回复锚点（eventID 与 seq）。
func (m *Message) ReplyEvent() (eventID string, seq int) { return m.replyEv, m.seq }

// Validate 做发送前的本地协议校验。
func (m *Message) Validate() error {
	if m == nil {
		return errors.New("send: nil message")
	}
	// 被动回复锚点：msg_id 与 event_id 二选一，seq 必须从 1 开始。
	if m.replyID != "" && m.replyEv != "" {
		return errors.New("send: msg_id 与 event_id 锚点只能二选一")
	}
	// 互动召回消息与被动回复锚点互斥（官方：is_wakeup 与 msg_id/event_id 互斥使用）。
	if m.wakeup && (m.replyID != "" || m.replyEv != "") {
		return errors.New("send: is_wakeup 与 msg_id/event_id 锚点互斥，不能同时设置")
	}
	if m.forceVerifyImage && m.kind != KindMarkdown {
		return errors.New("send: force_verify_image_resource 仅对 Markdown 消息有效")
	}
	if m.seq < 0 {
		return fmt.Errorf("send: msg_seq 不能为负数（当前 %d）", m.seq)
	}
	if (m.replyID != "" || m.replyEv != "") && m.seq < 1 {
		return fmt.Errorf("send: msg_seq 必须从 1 开始单调递增（当前 %d）", m.seq)
	}
	switch m.kind {
	case KindText:
		if strings.TrimSpace(m.text) == "" {
			return errors.New("send: 文本内容不能为空")
		}
	case KindMarkdown:
		if m.mdTpl == nil {
			if strings.TrimSpace(m.text) == "" {
				return errors.New("send: 文本内容不能为空")
			}
		} else {
			if strings.TrimSpace(m.mdTpl.ID) == "" {
				return errors.New("send: markdown 模板 id 不能为空")
			}
			for i, p := range m.mdTpl.Params {
				if strings.TrimSpace(p.Key) == "" {
					return fmt.Errorf("send: markdown 模板第 %d 个参数 key 为空", i+1)
				}
			}
		}
	case KindMedia:
		if strings.TrimSpace(m.media) == "" {
			return errors.New("send: 富媒体 file_info 不能为空")
		}
	case KindCard:
		return validateCard(m.card)
	default:
		return fmt.Errorf("send: 未知消息类型 %d", m.kind)
	}
	if m.keyboard != nil && m.kbID != "" {
		return errors.New("send: 自定义 keyboard 与 keyboard 模板 id 只能二选一")
	}
	if m.keyboard != nil {
		return validateKeyboard(*m.keyboard)
	}
	return nil
}

// validateCard 校验卡片协议约束。
func validateCard(c Card) error {
	titleLen := utf8.RuneCountInString(c.Title)
	if titleLen < 1 || titleLen > maxTitleRunes {
		return fmt.Errorf("send: 卡片 title 长度必须在 1~%d（当前 %d）", maxTitleRunes, titleLen)
	}
	if descLen := utf8.RuneCountInString(c.Description); descLen < 1 || descLen > maxDescRunes {
		return fmt.Errorf("send: 卡片 desc 长度必须在 1~%d（当前 %d）", maxDescRunes, descLen)
	}
	if err := checkHTTPS("url", c.URL); err != nil {
		return err
	}
	return checkHTTPS("pic_url", c.PicURL)
}

// checkHTTPS 校验公网 https 链接（卡片要求公网可直连）。
func checkHTTPS(field, raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("send: 卡片 %s 必须是 https（当前 %q）", field, raw)
	}
	if len(raw) > maxURLLen {
		return fmt.Errorf("send: 卡片 %s 长度不能超过 %d", field, maxURLLen)
	}
	return nil
}

// validateKeyboard 校验内嵌键盘行数与按钮数。
func validateKeyboard(k Keyboard) error {
	if len(k.Rows) == 0 || len(k.Rows) > maxRows {
		return fmt.Errorf("send: 键盘行数必须在 1~%d（当前 %d）", maxRows, len(k.Rows))
	}
	seenIDs := make(map[string]struct{})
	for i, row := range k.Rows {
		if len(row) == 0 || len(row) > maxButtons {
			return fmt.Errorf("send: 键盘第 %d 行按钮数必须在 1~%d（当前 %d）", i+1, maxButtons, len(row))
		}
		for j, btn := range row {
			if strings.TrimSpace(btn.ID) == "" {
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮缺少 id（同一键盘内必须唯一）", i+1, j+1)
			}
			if _, dup := seenIDs[btn.ID]; dup {
				return fmt.Errorf("send: 键盘按钮 id %q 重复（同一键盘内必须唯一）", btn.ID)
			}
			seenIDs[btn.ID] = struct{}{}
			if utf8.RuneCountInString(btn.Label) < 1 || utf8.RuneCountInString(btn.Label) > maxLabelRunes {
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 label 长度必须在 1~%d（当前 %d）", i+1, j+1, maxLabelRunes, utf8.RuneCountInString(btn.Label))
			}
			switch btn.Style {
			case StyleGray, StyleBlue, StyleWhiteRed, StyleBlueWhite:
			default:
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 style 非法 %d（合法 0/1/3/4）", i+1, j+1, btn.Style)
			}
			switch btn.ActionType {
			case 0, 1, 2:
			default:
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 action.type 非法 %d（合法 0 跳转/1 回调/2 指令）", i+1, j+1, btn.ActionType)
			}
			if btn.ActionType == 0 && btn.Link == "" {
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮是跳转类型但 link 为空", i+1, j+1)
			}
			// 官方：type=1/2 时 data 必填（回调数据 / 指令内容）。
			if btn.ActionType != 0 && strings.TrimSpace(btn.ActionData) == "" {
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 action.type=%d 时 data 必填", i+1, j+1, btn.ActionType)
			}
			switch btn.PermissionType {
			case 0, 1, 2, 3:
			default:
				return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 permission_type 非法 %d（合法 0/1/2/3）", i+1, j+1, btn.PermissionType)
			}
			if btn.Modal != nil {
				if n := utf8.RuneCountInString(btn.Modal.Content); n < 1 || n > 40 {
					return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 modal.content 长度必须在 1~40（当前 %d）", i+1, j+1, n)
				}
				if strings.Contains(btn.Modal.Content, "http://") || strings.Contains(btn.Modal.Content, "https://") {
					return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 modal.content 不能包含 URL", i+1, j+1)
				}
				if n := utf8.RuneCountInString(btn.Modal.ConfirmText); n > 4 {
					return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 modal.confirm_text 最多 4 字符（当前 %d）", i+1, j+1, n)
				}
				if n := utf8.RuneCountInString(btn.Modal.CancelText); n > 4 {
					return fmt.Errorf("send: 键盘第 %d 行第 %d 个按钮 modal.cancel_text 最多 4 字符（当前 %d）", i+1, j+1, n)
				}
			}
		}
	}
	return nil
}
