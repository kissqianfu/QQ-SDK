// Package event 是归一化后的事件模型（平台语义，无业务字段）。
//
// 追踪 id 等业务字段由业务 adapter 自己补；SDK 只保证协议字段正确。
package event

import "time"

// Scene 是会话场景。
type Scene uint8

const (
	// SceneC2C 是单聊。
	SceneC2C Scene = iota + 1
	// SceneGroup 是群聊。
	SceneGroup
)

// Type 是平台事件类型。
type Type string

const (
	// C2CMessageCreate 是单聊消息。
	C2CMessageCreate Type = "C2C_MESSAGE_CREATE"
	// C2CMsgReject 是用户拒收/拉黑。
	C2CMsgReject Type = "C2C_MSG_REJECT"
	// C2CMsgReceive 是用户已接收回执（可作被动回复锚点）。
	C2CMsgReceive Type = "C2C_MSG_RECEIVE"
	// GroupAtMessageCreate 是群里 @ 机器人。
	GroupAtMessageCreate Type = "GROUP_AT_MESSAGE_CREATE"
	// GroupMessageCreate 是群内普通消息。
	GroupMessageCreate Type = "GROUP_MESSAGE_CREATE"
	// GroupAddRobot 是机器人加群。
	GroupAddRobot Type = "GROUP_ADD_ROBOT"
	// GroupDelRobot 是机器人退群。
	GroupDelRobot Type = "GROUP_DEL_ROBOT"
	// GroupMemberAdd 是群成员加入。
	GroupMemberAdd Type = "GROUP_MEMBER_ADD"
	// GroupMemberRemove 是群成员退出。
	GroupMemberRemove Type = "GROUP_MEMBER_REMOVE"
	// GroupJoinRequest 是用户申请加群（需管理员处理）。
	GroupJoinRequest Type = "GROUP_JOIN_REQUEST"
	// GroupMsgReceive 是群消息已接收回执（可作被动回复锚点）。
	GroupMsgReceive Type = "GROUP_MSG_RECEIVE"
	// GroupMsgReject 是群消息被拒收/拉黑。
	GroupMsgReject Type = "GROUP_MSG_REJECT"
	// FriendAdd 是添加好友。
	FriendAdd Type = "FRIEND_ADD"
	// FriendDel 是删除好友。
	FriendDel Type = "FRIEND_DEL"
	// SubscribeMessageStatus 是订阅消息状态变更。
	SubscribeMessageStatus Type = "SUBSCRIBE_MESSAGE_STATUS"
	// InteractionCreate 是按钮/指令回调。
	InteractionCreate Type = "INTERACTION_CREATE"
)

// 入群申请来源（GROUP_JOIN_REQUEST 的 apply_source）取值；与 API 侧列表接口同口径。
const (
	JoinApplySelf    = "self_apply" // 用户自己申请
	JoinApplyInvited = "invited"    // 被群成员邀请
)

// PartKind 是消息段类型。
type PartKind string

const (
	// PartText 是文本段。
	PartText PartKind = "text"
	// PartImage 是图片段。
	PartImage PartKind = "image"
	// PartVoice 是语音段。
	PartVoice PartKind = "voice"
	// PartVideo 是视频段。
	PartVideo PartKind = "video"
	// PartFile 是文件段。
	PartFile PartKind = "file"

	// Deprecated: 以下三个取值在 2026-11 的官方文档里没有定义，parse 也从不会产出
	// （接收侧附件只有 voice / image/* / video/mp4 / file）。保留仅为不破坏既有 switch。
	// PartFace 是 QQ 内置表情段。
	PartFace PartKind = "face"
	// PartMFace 是商城表情段。
	PartMFace PartKind = "mface"
	// PartAt 是 @ 段。
	PartAt PartKind = "at"
)

// Sender 是事件发起者；Bot 标记由平台直接给出，判断机器人不需要额外接口。
type Sender struct {
	ID         string // user_openid（单聊）或 member_openid（群聊）
	Name       string // author.username
	Bot        bool   // author.bot
	MemberRole string // member | admin | owner（群内角色，非 bot 权限体系）
}

// Part 是消息的一段内容。
type Part struct {
	Kind     PartKind
	Text     string
	URL      string
	MIME     string
	Duration time.Duration
	// Width/Height 是图片尺寸（仅图片附件，官方字段）。
	Width  int
	Height int
	// WavURL 是语音消息转好的 WAV 直链（仅语音附件，官方 voice_wav_url）。
	WavURL string
	// ASRText 是语音消息的官方 ASR 参考结果（仅语音附件，可能为空，官方 asr_refer_text）。
	ASRText string

	// Deprecated: FaceID / Duration 在 2026-11 的官方 MessageAttachment 里没有对应字段，
	// parse 也从不赋值。保留仅为不破坏既有代码。
	FaceID string
}

// Event 是归一化后的入站事件。
type Event struct {
	ID         string // 平台事件 id（幂等键）
	Type       Type
	Scene      Scene
	TargetID   string // group_openid / user_openid
	Sender     Sender
	MsgID      string // 被动回复锚点
	EventID    string // 被动回复锚点（event_id）：INTERACTION_CREATE / C2C_MSG_RECEIVE / FRIEND_ADD / GROUP_ADD_ROBOT / GROUP_MSG_RECEIVE
	AtMe       bool
	Parts      []Part
	RefIdx     string // 引用回复用：MessageScene.ext 的 msg_idx（机器人引用他人消息时传给 send.Message.Reference）
	RefMsgIdx  string // 本条消息所引用的消息索引（MessageScene.ext 的 ref_msg_idx，用户发引用消息时存在）
	Raw        []byte
	ReceivedAt time.Time
	// Timestamp 是平台事件时间（事件体 d.timestamp，RFC3339 / Unix 秒均归一化）。
	// ReceivedAt 是历史字段，取同一个平台时间；需要"本地收到时间"的业务请自行记录。
	Timestamp time.Time

	// MessageType 是消息内容类型（仅消息类事件）：
	// 0=普通文本、3=结构化卡片、101=并行消息、102=聊天记录、103=引用消息（官方 message_type）。
	MessageType int

	// InteractionType 是互动事件类型（仅 INTERACTION_CREATE）：
	// 11=消息按钮回调、12=快捷菜单回调（这两类必须调 Interact 回应）；
	// 13=消息反馈、14=清空会话、15=进出故事集、16=切换模型、18/19=授权、20=群授权状态变更（无需回应）。
	InteractionType int

	// Interaction 是 INTERACTION_CREATE 的 data.resolved（按钮 id/data、反馈选项、授权范围等）；
	// 非互动事件为 nil。
	Interaction *InteractionData

	// InteractionUserID 是互动事件的操作人 openid（resolved.user_id → group_member_openid → user_openid）。
	//
	// ⚠️ INTERACTION_CREATE 的事件体**没有 author**：群场景操作人在 d.group_member_openid，
	// 单聊在 d.user_openid。做按钮权限白名单时必须用它（或 Sender.ID），
	// 不要用 ev.Interaction.UserID——那个字段官方标注"仅频道场景有值"。
	InteractionUserID string

	// Mentions 是消息中 @ 的用户列表（官方 mentions，平台保证不含 @ 机器人自身；仅消息类事件）。
	Mentions []Sender

	// Ark 是结构化卡片数据（官方 ark_data，message_type=3 时有值）：
	// ark_type: tuwen=图文H5 / feed=图文卡片 / miniapp=小程序 / map=位置 /
	// contact_card=好友名片 / video_share=视频分享 / music_together=一起听歌 /
	// picture=图片（2026-09-16 新增）。
	Ark *ArkData

	// Elements 是消息元素列表（官方 msg_elements，message_type=103 引用消息时含被引用内容，可递归嵌套）。
	Elements []MessageElement

	// JoinRequest 仅 GROUP_JOIN_REQUEST（机器人是群管理员时下发）：
	// JoinRequestID 是审批接口 /approval_join_request 的必填参数。
	JoinRequest *JoinRequest

	// SubscribeResults 仅 SUBSCRIBE_MESSAGE_STATUS：每个订阅模板的授权结果。
	SubscribeResults []SubscribeResult

	// FriendScene / SceneParam 仅 FRIEND_ADD：scene 是添加来源场景值（1000/1001/...），
	// SceneParam 是开发者生成分享链接时传入的 callback_data 透传值。
	FriendScene string
	SceneParam  string

	// GroupMemberOpenID 仅群场景互动事件（INTERACTION_CREATE 的 group_member_openid）。
	GroupMemberOpenID string
}

// InteractionData 是互动事件的解析后数据（官方 data.resolved）。
type InteractionData struct {
	// ButtonID / ButtonData 是"用户点了哪个按钮"的唯一依据（type=11 消息按钮）。
	ButtonID   string
	ButtonData string
	UserID     string // 仅频道场景
	FeatureID  string // 仅快捷菜单（管理端配置的功能 ID）
	MessageID  string // 操作的消息 ID
	// FeedbackOpt 仅 type=13：LIKE=点赞 / UNLIKE=点踩。
	FeedbackOpt string
	// Checked 仅 type=13：反馈选项是否选中。
	Checked bool
	// Action 仅 type=15/16：ENTER_STORY / QUIT_STORY 等操作动作。
	Action string
	// MessageSceneExt 仅 type=13：message_scene.ext 的键值对（如 "disable_net_search=1"）。
	MessageSceneExt []string
	// AuthorizeData 仅 type=18/19：用户/群授权数据。
	AuthorizeData *AuthorizeData
}

// AuthorizeData 是授权事件的数据（官方 authorize_data）。
type AuthorizeData struct {
	// OptScene 授权操作场景：setting=资料页设置 / dialog=弹窗授权。
	OptScene string
	// Scope 授权范围：c2c_push=C2C 主动消息推送 / group_push=群主动消息推送。
	Scope string
}

// JoinRequest 是入群申请（官方 GROUP_JOIN_REQUEST 事件体）。
type JoinRequest struct {
	// JoinRequestID 审批接口的必填参数。
	JoinRequestID string
	MemberOpenID  string
	Username      string
	ApplyAt       string
	// ApplySource 申请来源：self_apply / invited。
	ApplySource string
	InvitedBy   string
	VerifyInfo  *VerifyInfo
	// AutoApprovedStrategyID 自动审批命中的策略 ID（官方 auto_approved.strategy_id）。
	AutoApprovedStrategyID string
}

// VerifyInfo 是入群申请的验证信息。
type VerifyInfo struct {
	Method        string
	VerifyMessage string
	ReviewQAList  []VerifyQA
}

// VerifyQA 是入群验证的一对问答。
type VerifyQA struct {
	Question string
	Answer   string
}

// SubscribeResult 是订阅消息授权状态（官方 SubscribeMsgTemplateResult）。
type SubscribeResult struct {
	TemplateID       int    // 平台订阅模板 ID
	CustomTemplateID string // 自定义订阅模板 ID
	Op               int    // 1=允许订阅 / 2=拒绝订阅
	// SubscribeID 发送订阅消息时使用的订阅 ID。
	SubscribeID string
	SubscribeTS int64
	UpdateTS    int64
}

// ArkData 是结构化卡片消息数据（官方 ark_data；fields 的常见键：title/desc/jump_url/preview/
// source/source_logo/tag/tags/nickname/avatar/address 等，由卡片类型决定）。
type ArkData struct {
	Prompt  string         `json:"prompt"` // 卡片中的用户操作提示文本
	ArkType string         `json:"ark_type"`
	ArkName string         `json:"ark_name"` // 卡片类型中文名，如"小程序"
	Fields  map[string]any `json:"fields"`
}

// MessageElement 是消息元素（官方 msg_elements，递归结构）。
type MessageElement struct {
	MsgIdx      string // 该元素对应的消息索引（引用场景用）
	Author      Sender
	MessageType int
	Parts       []Part // 正文 + 附件（与 Event.Parts 同一归一化规则）
	Ark         *ArkData
	Elements    []MessageElement // 嵌套消息元素
}
