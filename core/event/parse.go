package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kissqianfu/QQ-SDK/common/qqtypes"
)

// Envelope 是平台回调 / 网关消息的外层结构（webhook 与 ws 共用一份定义）。
type Envelope struct {
	ID string          `json:"id"`
	Op int             `json:"op"`
	S  int64           `json:"s"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
}

// Parse 把平台原始 payload 解析成 Event。
// 未知 t 不报错（Type 保留原文，业务进死信表）；只有消息类事件的 d 结构坏才返回错误
// （消息丢了必须让传输层感知，不能变幽灵事件）；控制事件 d 异常容忍（保留事件头）。
func Parse(raw []byte) (*Event, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("event: decode envelope: %w", err)
	}
	return ParseEnvelope(&env)
}

// ParseEnvelope 是已解出信封的解析入口：ws / webhook 各自已解过一次信封，直接传入
// 免去「序列化回 JSON 再反序列化」的重复开销。
func ParseEnvelope(env *Envelope) (*Event, error) {
	if env == nil {
		return nil, fmt.Errorf("event: nil envelope")
	}
	d, err := decodeEventData(env)
	if err != nil {
		return nil, err
	}

	// 场景判定：默认按事件类型前缀；互动事件按 d.scene（官方字段：c2c=单聊, group=群聊）。
	ev := &Event{
		ID:          env.ID,
		Type:        Type(env.T),
		MessageType: d.MessageType,
		Scene:       sceneOf(env.T),
		AtMe:        env.T == string(GroupAtMessageCreate),
	}

	// 被动回复锚点是事件最外层的 id，不要用 d.id（互动事件的 d.id 是裸 interaction_id）。
	// Event.ID/EventID = 外层 id；Event.InteractionID = 裸 interaction_id（回应专用）。
	if SupportsEventIDReply(ev.Type) {
		ev.EventID = env.ID
	}
	if ev.Type == InteractionCreate {
		fillInteraction(ev, d, env.ID)
	}

	ev.TargetID = d.targetID(ev.Scene)
	ev.GroupID = d.GroupID
	ev.UnionGroupOpenID = d.UnionGroupOpenID
	ev.Sender = resolveSender(d, ev)
	if ev.TargetID == "" && ev.Scene == SceneC2C {
		ev.TargetID = d.OpenID
	}
	// MsgID 只对消息类事件赋值（其他事件的 d.id 含义不同）。
	if IsMessageType(ev.Type) {
		ev.MsgID = d.ID
	}
	ev.RefIdx, ev.RefMsgIdx = d.refIdxs()
	ev.SceneSource = d.MessageScene.Source
	ev.Parts = d.parts()
	ev.Mentions = d.mentions(ev.Scene)
	if ev.Type != GroupAtMessageCreate {
		// 全量群消息按 mentions[].is_you 判定是否 @ 了自己（官方标记，免比对 openid）。
		for _, m := range ev.Mentions {
			if m.IsYou {
				ev.AtMe = true
				break
			}
		}
	}
	if d.Ark != nil {
		ev.Ark = d.Ark.model()
	}
	ev.Elements = elementsFrom(d.Elements, 0, ev.Scene)
	if ts := qqtypes.ParseTime(d.Timestamp); !ts.IsZero() {
		// ReceivedAt 是历史字段（取平台事件时间，保留旧语义）；Timestamp 是同一值的明确命名。
		ev.ReceivedAt = ts
		ev.Timestamp = ts
	}

	fillTypeSpecific(ev, d)
	return ev, nil
}

// decodeEventData 解出 d 字段并拦下"空事件体"：
// "d":null / "d":{} / 空 d 对消息事件同样解码成功，但会产出空壳幽灵事件（业务回复必然失败，
// 且错误被推迟到发送阶段才发现）。只拦"完全空"，不逐字段校验（平台会按场景省略字段）。
func decodeEventData(env *Envelope) (dispatchData, error) {
	var d dispatchData
	if env.T == "" {
		return d, fmt.Errorf("event: missing event type (t)")
	}
	if len(env.D) > 0 {
		if err := json.Unmarshal(env.D, &d); err != nil {
			// 消息类事件 d 坏了必须报错；控制事件容忍（RESUMED 等的 d 是字符串/空）。
			if IsMessageType(Type(env.T)) {
				return d, fmt.Errorf("event: decode %s payload: %w", env.T, err)
			}
		}
	}
	if IsMessageType(Type(env.T)) && isEmptyPayload(env.D) {
		return d, fmt.Errorf("event: %s 的事件体为空（d 缺失/null/{}）", env.T)
	}
	return d, nil
}

// isEmptyPayload 判断 d 是否"完全空"：缺失、null，或压掉空白后没有任何字段的对象。
func isEmptyPayload(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return true
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, trimmed); err != nil {
		return false // 非法 JSON 由 decodeEventData 的解码分支报错
	}
	return compacted.String() == "{}"
}

// fillInteraction 填互动事件专有字段：interaction_id、类型、场景与操作者。
func fillInteraction(ev *Event, d dispatchData, envID string) {
	// 官方注明 interaction_id 不带 "INTERACTION_CREATE:" 前缀；历史事件出现过带前缀形态，防御性剥掉。
	ev.InteractionID = strings.TrimPrefix(qqtypes.FirstNonEmpty(d.ID, envID), "INTERACTION_CREATE:")
	ev.InteractionType = d.InteractionType
	if s := sceneFromString(d.sceneValue()); s != 0 {
		ev.Scene = s
	} else if d.ChatType == 1 {
		ev.Scene = SceneGroup
	} else if d.ChatType == 2 {
		ev.Scene = SceneC2C
	}
	ev.GroupMemberOpenID = d.GroupMemberOID
	if d.Data != nil && d.Data.Resolved != nil {
		ev.Interaction = d.Data.Resolved.model()
		ev.InteractionUserID = qqtypes.FirstNonEmpty(d.Data.Resolved.UserID, d.GroupMemberOID, d.UserOpenID)
		return
	}
	ev.InteractionUserID = qqtypes.FirstNonEmpty(d.GroupMemberOID, d.UserOpenID)
}

// resolveSender 定发送者：author 优先，操作类事件取 op_member_openid，
// 都没有时按各事件的顶层 openid 兜底。
func resolveSender(d dispatchData, ev *Event) Sender {
	sender := d.Author.sender(ev.Scene)
	switch ev.Type {
	case GroupAddRobot, GroupDelRobot, GroupMsgReject:
		// 这些事件体只有 op_member_openid（操作成员），author 缺失。
		if id := d.OpMemberOpenID; id != "" {
			return Sender{ID: id}
		}
	}
	// FRIEND_ADD/DEL 的 openid 在顶层 d.openid；GROUP_MEMBER_ADD/REMOVE、GROUP_JOIN_REQUEST
	// 的成员 openid 在 d.member_openid；INTERACTION_CREATE 没有 author。
	if sender.ID == "" {
		sender.ID = qqtypes.FirstNonEmpty(d.OpenID, d.MemberOpenID, d.GroupMemberOID, d.UserOpenID)
	}
	return sender
}

// fillTypeSpecific 填各事件独占的字段（字段不同、互不影响）。
func fillTypeSpecific(ev *Event, d dispatchData) {
	switch ev.Type {
	case FriendAdd:
		ev.FriendScene = d.sceneValue()
		ev.SceneParam = d.SceneParam
		ev.FriendShortCode = d.ShortCode
	case GroupJoinRequest:
		ev.JoinRequest = d.joinRequest()
	case SubscribeMessageStatus:
		ev.SubscribeResults = d.subscribeResults()
	case InteractionCreate:
		ev.ChatType = d.ChatType
		ev.ApplicationID = d.ApplicationID
	}
}

// dispatchData 是 d 字段的并集（不同事件类型字段不同，缺的字段自然为零值）。
// scene 兼容两种类型（互动事件是字符串、FRIEND_ADD 是整数），用 json.RawMessage 兜住。
type dispatchData struct {
	ID              string          `json:"id"`
	OpenID          string          `json:"openid"`       // FRIEND_ADD/DEL 的用户 openid（官方字段名是 openid，不是 user_openid）
	InteractionType int             `json:"type"`         // INTERACTION_CREATE 的互动类型（11/12/13...）
	Scene           json.RawMessage `json:"scene"`        // INTERACTION_CREATE: c2c/group；FRIEND_ADD: 整数值
	SceneParam      string          `json:"scene_param"`  // FRIEND_ADD: 分享链接透传的 callback_data
	MessageType     int             `json:"message_type"` // 消息内容类型：0=文本 3=结构化卡片 101=并行消息 102=聊天记录 103=引用消息
	Content         string          `json:"content"`
	GroupOpenID     string          `json:"group_openid"`
	// GroupID 与 group_openid 并列下发（2026-11-18 真机确认，值相同），文档未列。
	GroupID string `json:"group_id"`
	// UnionGroupOpenID 跨应用关联群 openid（群消息、进退群、群成员增减等事件下发，
	// 绑定三方应用时有效；2026-11-16 起新增）。
	UnionGroupOpenID string          `json:"union_group_openid"`
	UserOpenID       string          `json:"user_openid"`
	OpMemberOpenID   string          `json:"op_member_openid"`    // GROUP_ADD/DEL_ROBOT、GROUP_MSG_REJECT：操作成员
	GroupMemberOID   string          `json:"group_member_openid"` // INTERACTION_CREATE 群场景的成员 openid
	MemberOpenID     string          `json:"member_openid"`       // GROUP_MEMBER_ADD/REMOVE、GROUP_JOIN_REQUEST
	Timestamp        json.RawMessage `json:"timestamp"`
	Author           authorData      `json:"author"`
	Attachments      []attachment    `json:"attachments"`
	Mentions         []authorData    `json:"mentions"` // 消息中 @ 的用户列表（可含机器人自身，is_you 标记是否 @ 自己）
	Ark              *arkData        `json:"ark_data"` // 结构化卡片（message_type=3）
	Elements         []elementData   `json:"msg_elements"`
	MessageScene     struct {
		// Source 是场景来源（2026-11-18 文档确认：default=默认聊天窗口）。
		Source string            `json:"source"`
		Ext    []json.RawMessage `json:"ext"`
	} `json:"message_scene"`

	// GROUP_JOIN_REQUEST
	JoinRequestID string           `json:"join_request_id"`
	Username      string           `json:"username"`
	ApplyAt       string           `json:"apply_at"`
	ApplySource   string           `json:"apply_source"`
	InvitedBy     string           `json:"invited_by"`
	VerifyInfo    *verifyInfoData  `json:"verify_info"`
	AutoApproved  *autoApprovedDat `json:"auto_approved"`
	// RiskTips / UnionOpenID / ApplicantBot 是 2026-11-18 文档确认的顶层字段：
	// risk_tips=安全提示语；union_openid=申请人在应用下的统一标识；bot=申请人是否机器人账号
	// （官方语义标注含糊，仅建模防漂移，不建议依赖）。
	RiskTips     string `json:"risk_tips"`
	UnionOpenID  string `json:"union_openid"`
	ApplicantBot bool   `json:"bot"`

	// FRIEND_ADD
	// ShortCode 是机器人分享链接的短链 code（2026-11-18 文档确认）。
	ShortCode string `json:"short_code"`

	// INTERACTION_CREATE 的补充字段（2026-11-18 文档确认）。
	ChatType      int    `json:"chat_type"`      // 0=频道 1=群聊 2=单聊
	ApplicationID string `json:"application_id"` // 机器人 AppID

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
	// 互动类型统一取外层 d.type（见 dispatchData.InteractionType），这里不再重复建模。
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
		// Switch 仅第二帧起携带（2026-11-18 真机确认）：true=授权已开启。
		Switch *bool `json:"switch"`
	} `json:"authorize_data"`
}

// authorData 是事件里的 d.author（完整 User 对象，自带 bot 标记）。
type authorData struct {
	ID             string `json:"id"`
	UserOpenID     string `json:"user_openid"`
	MemberOpenID   string `json:"member_openid"`
	Username       string `json:"username"`
	Avatar         string `json:"avatar"`          // 用户头像 URL（2026-11-16 起下发）
	UnionOpenID    string `json:"union_openid"`    // 跨应用关联用户 openid（绑定三方应用时有效）
	MemberNickname string `json:"member_nickname"` // 群内昵称（仅群聊消息，2026-11-16 起下发）
	Bot            bool   `json:"bot"`
	MemberRole     string `json:"member_role"`
	// IsYou / Scope 仅 mentions 数组元素有意义（2026-11-18 文档确认）：
	// is_you=是否 @ 的当前 bot；scope=all=@全体成员 / single=指定 @ 对象。
	IsYou bool   `json:"is_you"`
	Scope string `json:"scope"`
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

// sender 归一化发送者：群聊优先 member_openid（平台 @ 语义用它），单聊取 user_openid，兜底 id。
// 群消息里 user_openid 与 member_openid 可能同时存在，选错会让权限白名单比对口径漂移。
func (a authorData) sender(scene Scene) Sender {
	var id string
	if scene == SceneGroup {
		id = qqtypes.FirstNonEmpty(a.MemberOpenID, a.UserOpenID, a.ID)
	} else {
		id = qqtypes.FirstNonEmpty(a.UserOpenID, a.MemberOpenID, a.ID)
	}
	return Sender{
		ID: id, Name: a.Username, Avatar: a.Avatar,
		MemberNickname: a.MemberNickname, UnionOpenID: a.UnionOpenID,
		Bot: a.Bot, MemberRole: a.MemberRole,
		IsYou: a.IsYou, Scope: a.Scope,
	}
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
func elementsFrom(raw []elementData, depth int, scene Scene) []MessageElement {
	if len(raw) == 0 || depth > maxElementDepth {
		return nil
	}
	out := make([]MessageElement, 0, len(raw))
	for _, e := range raw {
		me := MessageElement{
			MsgIdx:      e.MsgIdx,
			Author:      e.Author.sender(scene),
			MessageType: e.MessageType,
			Parts:       partsOf(e.Content, e.Attachments),
		}
		if e.Ark != nil {
			me.Ark = e.Ark.model()
		}
		me.Elements = elementsFrom(e.Elements, depth+1, scene)
		out = append(out, me)
	}
	return out
}

// mentions 归一化 @ 用户列表。
func (d dispatchData) mentions(scene Scene) []Sender {
	if len(d.Mentions) == 0 {
		return nil
	}
	out := make([]Sender, 0, len(d.Mentions))
	for _, m := range d.Mentions {
		out = append(out, m.sender(scene))
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
			WavURL: a.VoiceWavURL, ASRText: a.ASRReferText, Filename: a.Filename, Size: a.Size}
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

// refIdxs 解析 message_scene.ext：返回 msg_idx（引用回复用）与 ref_msg_idx。
// 兼容历史 JSON 对象形态与裸字符串（视为 msg_idx）。
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
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
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
			// 未知 key（如官方同时下发的 auth_token=tok）必须丢弃：
			// 若落回裸字符串兜底，会把垃圾写进 msg_idx，并让真正的 msg_idx 再无线索。
			// 唯一例外是裸 base64 值：它只在尾部有 "=" 填充，去掉填充后不含 "="。
			if strings.Contains(strings.TrimRight(s, "="), "=") {
				continue
			}
			if msgIdx == "" {
				msgIdx = s
			}
			continue
		}
		// 历史 JSON 对象形态兼容。
		if strings.HasPrefix(s, "{") {
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

// sceneValue 取 d.scene 的字符串形态：字符串原样返回；整数（FRIEND_ADD 的场景值）
// 去引号取字面量——一个函数同时覆盖互动事件与好友添加两种场景口径。
func (d dispatchData) sceneValue() string {
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
		RiskTips:      d.RiskTips,
		UnionOpenID:   d.UnionOpenID,
		ApplicantBot:  d.ApplicantBot,
	}
	if d.VerifyInfo != nil {
		vi := &VerifyInfo{Method: d.VerifyInfo.Method, VerifyMessage: d.VerifyInfo.VerifyMessage}
		for _, qa := range d.VerifyInfo.ReviewQAList {
			// verifyQAData 与 VerifyQA 字段完全一致，直接类型转换（避免逐字段抄写）。
			vi.ReviewQAList = append(vi.ReviewQAList, VerifyQA(qa))
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
		// subscribeResultData 与 SubscribeResult 字段完全一致，直接类型转换。
		out = append(out, SubscribeResult(r))
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
		out.AuthorizeData = &AuthorizeData{OptScene: r.AuthorizeData.OptScene, Scope: r.AuthorizeData.Scope, Switch: r.AuthorizeData.Switch}
	}
	return out
}
