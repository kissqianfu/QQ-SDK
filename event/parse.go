package event

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kissqianfu/QQ-SDK/qqtypes"
)

// Envelope 是平台回调 / 网关消息的外层结构（webhook 与 ws 共用一份定义）。
type Envelope struct {
	ID string          `json:"id"`
	Op int             `json:"op"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
}

// Parse 把平台原始 payload 解析成 Event。
//
// 纪律：
//   - 未知 t 不报错：Type 保留原值、Raw 保留原文（业务进死信表，不能静默丢）；
//   - Only 结构坏（不是 JSON、缺 t）才返回错误；
//   - AtMe 只按事件类型判定：GROUP_AT_MESSAGE_CREATE 平台保证是真 @（mentions 不含机器人自身）。
func Parse(raw []byte) (*Event, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("event: decode envelope: %w", err)
	}
	if env.T == "" {
		return nil, fmt.Errorf("event: missing event type (t)")
	}

	var d dispatchData
	if len(env.D) > 0 {
		// d 通常是对象；RESUMED 等控制事件的 d 是字符串/空——不能因此丢掉事件头。
		_ = json.Unmarshal(env.D, &d)
	}

	ev := &Event{
		ID:          env.ID,
		Type:        Type(env.T),
		MessageType: d.MessageType,
		Raw:         raw,
	}
	// 场景判定：默认按事件类型前缀；互动事件按 d.scene（官方字段：c2c=单聊, group=群聊）。
	ev.Scene = sceneOf(env.T)
	ev.AtMe = env.T == string(GroupAtMessageCreate)

	// event_id 被动回复锚点：官方"从事件最外层的 id 获取"，
	// 支持事件：INTERACTION_CREATE（取 d.id）、GROUP_ADD_ROBOT、GROUP_MSG_RECEIVE
	// （后两个 d 里没有 id，取外层）；单聊还支持 C2C_MSG_RECEIVE、FRIEND_ADD。
	switch ev.Type {
	case InteractionCreate:
		// 官方注明 interaction_id 不带 "INTERACTION_CREATE:" 前缀；历史事件出现过带前缀形态，防御性剥掉。
		ev.EventID = qqtypes.FirstNonEmpty(strings.TrimPrefix(d.ID, "INTERACTION_CREATE:"), env.ID)
		ev.InteractionType = d.InteractionType
		if s := sceneFromString(d.sceneString()); s != 0 {
			ev.Scene = s
		}
		ev.GroupMemberOpenID = d.GroupMemberOID
		if d.Data != nil && d.Data.Resolved != nil {
			ev.Interaction = d.Data.Resolved.model()
			ev.InteractionUserID = qqtypes.FirstNonEmpty(d.Data.Resolved.UserID, d.GroupMemberOID, d.UserOpenID)
		} else {
			ev.InteractionUserID = qqtypes.FirstNonEmpty(d.GroupMemberOID, d.UserOpenID)
		}
	case GroupAddRobot:
		ev.EventID = qqtypes.FirstNonEmpty(d.ID, env.ID)
	case GroupMsgReceive:
		ev.EventID = qqtypes.FirstNonEmpty(d.ID, env.ID)
	case C2CMsgReceive, FriendAdd:
		// 单聊场景官方支持 event_id 回复的事件是 INTERACTION_CREATE / C2C_MSG_RECEIVE /
		// FRIEND_ADD（群聊是 INTERACTION_CREATE / GROUP_ADD_ROBOT / GROUP_MSG_RECEIVE）。
		// 都取"事件最外层的 id"；d.id 只是防御性兜底。
		ev.EventID = qqtypes.FirstNonEmpty(d.ID, env.ID)
	}

	ev.TargetID = d.targetID(ev.Scene)
	ev.Sender = d.Author.sender()
	switch ev.Type {
	case GroupAddRobot, GroupDelRobot, GroupMsgReject:
		// 这些事件体只有 op_member_openid（操作成员），author 缺失。
		if id := d.OpMemberOpenID; id != "" {
			ev.Sender = Sender{ID: id}
		}
	}
	// FRIEND_ADD/DEL 的 author 只有 union_openid，用户 openid 在顶层 d.openid。
	// GROUP_MEMBER_ADD/REMOVE / GROUP_JOIN_REQUEST 的成员 openid 在 d.member_openid。
	// INTERACTION_CREATE 没有 author：群场景在 d.group_member_openid，单聊在 d.user_openid。
	if ev.Sender.ID == "" {
		ev.Sender.ID = qqtypes.FirstNonEmpty(d.OpenID, d.MemberOpenID, d.GroupMemberOID, d.UserOpenID)
	}
	if ev.TargetID == "" && ev.Scene == SceneC2C {
		ev.TargetID = d.OpenID
	}
	// MsgID 只对消息类事件赋值：其他事件（互动/成员/申请…）的 d.id 含义不同，
	// 混进 MsgID 会被业务误当成被动回复锚点。
	switch ev.Type {
	case C2CMessageCreate, GroupAtMessageCreate, GroupMessageCreate:
		ev.MsgID = d.ID
	}
	ev.RefIdx, ev.RefMsgIdx = d.refIdxs()
	ev.Parts = d.parts()
	ev.Mentions = d.mentions()
	if d.Ark != nil {
		ev.Ark = d.Ark.model()
	}
	ev.Elements = elementsFrom(d.Elements, 0)
	if ts := qqtypes.ParseTime(d.Timestamp); !ts.IsZero() {
		// ReceivedAt 是历史字段（取平台事件时间，保留旧语义）；Timestamp 是同一值的明确命名。
		ev.ReceivedAt = ts
		ev.Timestamp = ts
	}

	switch ev.Type {
	case FriendAdd:
		ev.FriendScene = d.friendScene()
		ev.SceneParam = d.SceneParam
	case GroupJoinRequest:
		ev.JoinRequest = d.joinRequest()
	case SubscribeMessageStatus:
		ev.SubscribeResults = d.subscribeResults()
	}
	return ev, nil
}

// dispatchData 是 d 字段的并集（不同事件类型字段不同，缺的字段自然为零值）。
//
// ⚠️ scene 在两种事件里类型不同：INTERACTION_CREATE 是字符串（c2c/group/guild），
// FRIEND_ADD 是整数场景值（1000/1001/...）。用 json.RawMessage 兜住，避免
// "一个字段类型不匹配导致整包解析失败、所有字段一起丢"。
type dispatchData struct {
	ID              string          `json:"id"`
	OpenID          string          `json:"openid"`       // FRIEND_ADD/DEL 的用户 openid（官方字段名是 openid，不是 user_openid）
	InteractionType int             `json:"type"`         // INTERACTION_CREATE 的互动类型（11/12/13...）
	Scene           json.RawMessage `json:"scene"`        // INTERACTION_CREATE: c2c/group；FRIEND_ADD: 整数值
	SceneParam      string          `json:"scene_param"`  // FRIEND_ADD: 分享链接透传的 callback_data
	MessageType     int             `json:"message_type"` // 消息内容类型：0=文本 3=结构化卡片 101=并行消息 102=聊天记录 103=引用消息
	Content         string          `json:"content"`
	GroupOpenID     string          `json:"group_openid"`
	UserOpenID      string          `json:"user_openid"`
	OpMemberOpenID  string          `json:"op_member_openid"`    // GROUP_ADD/DEL_ROBOT、GROUP_MSG_REJECT：操作成员
	GroupMemberOID  string          `json:"group_member_openid"` // INTERACTION_CREATE 群场景的成员 openid
	MemberOpenID    string          `json:"member_openid"`       // GROUP_MEMBER_ADD/REMOVE、GROUP_JOIN_REQUEST
	ChannelID       string          `json:"channel_id"`
	Timestamp       json.RawMessage `json:"timestamp"`
	Author          authorData      `json:"author"`
	Attachments     []attachment    `json:"attachments"`
	Mentions        []authorData    `json:"mentions"` // 消息中 @ 的用户列表（不含 @ 机器人自身）
	Ark             *arkData        `json:"ark_data"` // 结构化卡片（message_type=3）
	Elements        []elementData   `json:"msg_elements"`
	MessageScene    struct {
		Ext []json.RawMessage `json:"ext"`
	} `json:"message_scene"`

	// GROUP_JOIN_REQUEST
	JoinRequestID string           `json:"join_request_id"`
	Username      string           `json:"username"`
	ApplyAt       string           `json:"apply_at"`
	ApplySource   string           `json:"apply_source"`
	InvitedBy     string           `json:"invited_by"`
	VerifyInfo    *verifyInfoData  `json:"verify_info"`
	AutoApproved  *autoApprovedDat `json:"auto_approved"`

	// SUBSCRIBE_MESSAGE_STATUS
	Result []subscribeResultData `json:"result"`

	// INTERACTION_CREATE 的 data（含 resolved）
	Data *interactionData `json:"data"`
}

// verifyInfoData 是入群申请的验证信息。
type verifyInfoData struct {
	Method        string         `json:"method"`
	VerifyMessage string         `json:"verify_message"`
	ReviewQAList  []verifyQAData `json:"review_qa_list"`
}

// verifyQAData 是入群验证问答。
type verifyQAData struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// autoApprovedDat 是自动审批命中的策略。
type autoApprovedDat struct {
	StrategyID string `json:"strategy_id"`
}

// subscribeResultData 是订阅消息模板的授权结果。
type subscribeResultData struct {
	TemplateID       int    `json:"template_id"`
	CustomTemplateID string `json:"custom_template_id"`
	Op               int    `json:"op"`
	SubscribeID      string `json:"subscribe_id"`
	SubscribeTS      int64  `json:"subscribe_ts"`
	UpdateTS         int64  `json:"update_ts"`
}

// interactionData 是 INTERACTION_CREATE 的 d.data。
type interactionData struct {
	Type     int                    `json:"type"`
	Resolved *interactionResolvedIn `json:"resolved"`
}

// interactionResolvedIn 是官方 data.resolved。
type interactionResolvedIn struct {
	ButtonData   string `json:"button_data"`
	ButtonID     string `json:"button_id"`
	UserID       string `json:"user_id"`
	FeatureID    string `json:"feature_id"`
	MessageID    string `json:"message_id"`
	FeedbackOpt  string `json:"feedback_opt"`
	Checked      int    `json:"checked"`
	Action       string `json:"action"`
	MessageScene struct {
		Ext []string `json:"ext"`
	} `json:"message_scene"`
	AuthorizeData *struct {
		OptScene string `json:"opt_scene"`
		Scope    string `json:"scope"`
	} `json:"authorize_data"`
}

// authorData 是事件里的 d.author（完整 User 对象，自带 bot 标记）。
type authorData struct {
	ID           string `json:"id"`
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	Bot          bool   `json:"bot"`
	MemberRole   string `json:"member_role"`
}

// attachment 是消息附件（图片/语音/视频/文件）。
type attachment struct {
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	Width       int    `json:"width"`  // 图片宽度（非图片附件无此字段）
	Height      int    `json:"height"` // 图片高度（非图片附件无此字段）
	Size        int64  `json:"size"`
	// VoiceWavURL 是语音消息转好的 WAV 文件直链（仅语音附件）。
	VoiceWavURL string `json:"voice_wav_url"`
	// ASRReferText 是语音消息的官方 ASR 参考结果（仅语音附件，可能为空）。
	ASRReferText string `json:"asr_refer_text"`
}

// sender 归一化发送者：单聊取 user_openid，群聊取 member_openid，兜底 id。
func (a authorData) sender() Sender {
	id := a.UserOpenID
	if id == "" {
		id = a.MemberOpenID
	}
	if id == "" {
		id = a.ID
	}
	return Sender{ID: id, Name: a.Username, Bot: a.Bot, MemberRole: a.MemberRole}
}

// arkData 是官方 ark_data（结构化卡片消息数据）。
type arkData struct {
	Prompt  string         `json:"prompt"`
	ArkType string         `json:"ark_type"`
	ArkName string         `json:"ark_name"`
	Fields  map[string]any `json:"fields"`
}

func (a *arkData) model() *ArkData {
	return &ArkData{Prompt: a.Prompt, ArkType: a.ArkType, ArkName: a.ArkName, Fields: a.Fields}
}

// elementData 是官方 msg_elements 元素（可递归嵌套）。
type elementData struct {
	MsgIdx      string        `json:"msg_idx"`
	Author      authorData    `json:"author"`
	MessageType int           `json:"message_type"`
	Content     string        `json:"content"`
	Attachments []attachment  `json:"attachments"`
	Ark         *arkData      `json:"ark_data"`
	Elements    []elementData `json:"msg_elements"`
}

// maxElementDepth 限制元素嵌套解析深度：官方引用嵌套很浅，护栏只为防病态 payload 打爆递归。
const maxElementDepth = 20

// elementsFrom 把官方 msg_elements 归一化成模型；depth 超限直接丢弃更深层（安全优先）。
func elementsFrom(raw []elementData, depth int) []MessageElement {
	if len(raw) == 0 || depth > maxElementDepth {
		return nil
	}
	out := make([]MessageElement, 0, len(raw))
	for _, e := range raw {
		me := MessageElement{
			MsgIdx:      e.MsgIdx,
			Author:      e.Author.sender(),
			MessageType: e.MessageType,
			Parts:       partsOf(e.Content, e.Attachments),
		}
		if e.Ark != nil {
			me.Ark = e.Ark.model()
		}
		me.Elements = elementsFrom(e.Elements, depth+1)
		out = append(out, me)
	}
	return out
}

// mentions 归一化 @ 用户列表。
func (d dispatchData) mentions() []Sender {
	if len(d.Mentions) == 0 {
		return nil
	}
	out := make([]Sender, 0, len(d.Mentions))
	for _, m := range d.Mentions {
		out = append(out, m.sender())
	}
	return out
}

// targetID 返回会话对端 id：群聊 group_openid，单聊用户 openid。
func (d dispatchData) targetID(scene Scene) string {
	if scene == SceneGroup {
		// 群事件严格取 group_openid：缺失就返回空，让业务侧报错而不是发错目标。
		return d.GroupOpenID
	}
	if d.UserOpenID != "" {
		return d.UserOpenID
	}
	if id := d.Author.UserOpenID; id != "" {
		return id
	}
	return d.Author.MemberOpenID
}

// parts 把 content 与 attachments 归一化成分段内容。
func (d dispatchData) parts() []Part {
	return partsOf(d.Content, d.Attachments)
}

// partsOf 是 content+attachments → []Part 的公共转换（dispatchData 与 msg_elements 共用）。
func partsOf(content string, atts []attachment) []Part {
	parts := make([]Part, 0, 1+len(atts))
	if content != "" {
		parts = append(parts, Part{Kind: PartText, Text: content})
	}
	for _, a := range atts {
		p := Part{URL: a.URL, MIME: a.ContentType, Width: a.Width, Height: a.Height,
			WavURL: a.VoiceWavURL, ASRText: a.ASRReferText}
		switch {
		case strings.HasPrefix(a.ContentType, "image/"):
			p.Kind = PartImage
		case strings.HasPrefix(a.ContentType, "audio/"), a.ContentType == "voice":
			p.Kind = PartVoice
		case strings.HasPrefix(a.ContentType, "video/"):
			p.Kind = PartVideo
		default:
			p.Kind = PartFile
		}
		parts = append(parts, p)
	}
	return parts
}

// refIdxs 解析 message_scene.ext：官方当前格式是 []string 的 "key=value"（示例 "msg_idx=REFIDX_xxx=="），
// 返回 msg_idx（引用回复用）与 ref_msg_idx（本条消息引用的是哪条消息）。
// 兼容历史 JSON 对象形态（{"msg_idx":...}）与裸字符串（视为 msg_idx）。
func (d dispatchData) refIdxs() (msgIdx, refMsgIdx string) {
	for _, raw := range d.MessageScene.Ext {
		s := strings.TrimSpace(string(raw))
		if s == "" || s == "null" {
			continue
		}
		if strings.HasPrefix(s, `"`) {
			var str string
			if err := json.Unmarshal(raw, &str); err == nil {
				s = strings.TrimSpace(str)
			}
		}
		if k, v, ok := strings.Cut(s, "="); ok {
			switch k {
			case "msg_idx":
				if msgIdx == "" {
					msgIdx = v
				}
				continue
			case "ref_msg_idx":
				if refMsgIdx == "" {
					refMsgIdx = v
				}
				continue
			}
			// 未知 key：不 continue，落回下面的对象/裸字符串兜底
			// （裸 base64 值自带 "=" padding，如 "REFIDX_ABC=="，会被误切出未知 key）。
		}
		// 历史 JSON 对象形态兼容。
		var obj struct {
			MsgIdx    string `json:"msg_idx"`
			RefMsgIdx string `json:"ref_msg_idx"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil {
			if msgIdx == "" {
				msgIdx = obj.MsgIdx
			}
			if refMsgIdx == "" {
				refMsgIdx = obj.RefMsgIdx
			}
			continue
		}
		// 裸字符串兜底：没有 key 前缀，直接当 msg_idx。
		if msgIdx == "" {
			msgIdx = s
		}
	}
	return msgIdx, refMsgIdx
}

// sceneOf 按事件类型前缀判断场景。
func sceneOf(t string) Scene {
	if strings.HasPrefix(t, "GROUP_") || strings.HasPrefix(t, "INTERACTION_") {
		return SceneGroup
	}
	return SceneC2C
}

// sceneFromString 按官方 d.scene 字段（c2c/group）判断场景；未知返回 0。
func sceneFromString(s string) Scene {
	switch s {
	case "group":
		return SceneGroup
	case "c2c":
		return SceneC2C
	default:
		return 0
	}
}

// sceneString 取 d.scene 的字符串形态（INTERACTION_CREATE 用）；非字符串返回空。
func (d dispatchData) sceneString() string {
	raw := strings.TrimSpace(string(d.Scene))
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(d.Scene, &s); err == nil {
		return s
	}
	return strings.Trim(raw, `"`)
}

// friendScene 取 FRIEND_ADD 的 scene 场景值（官方为整数 1000/1001/...）。
// 保留字符串形态（数字/引号数字都能读），业务按取值判断来源即可。
func (d dispatchData) friendScene() string {
	raw := strings.TrimSpace(string(d.Scene))
	if raw == "" || raw == "null" {
		return ""
	}
	return strings.Trim(raw, `"`)
}

// joinRequest 归一化 GROUP_JOIN_REQUEST 的事件体。
func (d dispatchData) joinRequest() *JoinRequest {
	if d.JoinRequestID == "" && d.MemberOpenID == "" {
		return nil
	}
	jr := &JoinRequest{
		JoinRequestID: d.JoinRequestID,
		MemberOpenID:  d.MemberOpenID,
		Username:      d.Username,
		ApplyAt:       d.ApplyAt,
		ApplySource:   d.ApplySource,
		InvitedBy:     d.InvitedBy,
	}
	if d.VerifyInfo != nil {
		vi := &VerifyInfo{Method: d.VerifyInfo.Method, VerifyMessage: d.VerifyInfo.VerifyMessage}
		for _, qa := range d.VerifyInfo.ReviewQAList {
			vi.ReviewQAList = append(vi.ReviewQAList, VerifyQA{Question: qa.Question, Answer: qa.Answer})
		}
		jr.VerifyInfo = vi
	}
	if d.AutoApproved != nil {
		jr.AutoApprovedStrategyID = d.AutoApproved.StrategyID
	}
	return jr
}

// subscribeResults 归一化 SUBSCRIBE_MESSAGE_STATUS 的 result[]。
func (d dispatchData) subscribeResults() []SubscribeResult {
	if len(d.Result) == 0 {
		return nil
	}
	out := make([]SubscribeResult, 0, len(d.Result))
	for _, r := range d.Result {
		out = append(out, SubscribeResult{
			TemplateID:       r.TemplateID,
			CustomTemplateID: r.CustomTemplateID,
			Op:               r.Op,
			SubscribeID:      r.SubscribeID,
			SubscribeTS:      r.SubscribeTS,
			UpdateTS:         r.UpdateTS,
		})
	}
	return out
}

// model 归一化官方 data.resolved。
func (r *interactionResolvedIn) model() *InteractionData {
	out := &InteractionData{
		ButtonID:        r.ButtonID,
		ButtonData:      r.ButtonData,
		UserID:          r.UserID,
		FeatureID:       r.FeatureID,
		MessageID:       r.MessageID,
		FeedbackOpt:     r.FeedbackOpt,
		Checked:         r.Checked != 0,
		Action:          r.Action,
		MessageSceneExt: r.MessageScene.Ext,
	}
	if r.AuthorizeData != nil {
		out.AuthorizeData = &AuthorizeData{OptScene: r.AuthorizeData.OptScene, Scope: r.AuthorizeData.Scope}
	}
	return out
}
