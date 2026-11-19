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
)

// Sender 是事件发起者；Bot 标记由平台直接给出，判断机器人不需要额外接口。
type Sender struct {
	ID         string // user_openid（单聊）或 member_openid（群聊）
	Name       string // author.username
	Bot        bool   // author.bot
	MemberRole string // member | admin | owner（群内角色，非 bot 权限体系）
	// Avatar 是发送者头像 URL（2026-11-16 起随消息事件下发，旧事件可能为空）。
	Avatar string
	// MemberNickname 是发送者的群内昵称（仅群聊消息事件，2026-11-16 起下发）。
	MemberNickname string
	// UnionOpenID 是跨应用关联用户 openid（绑定三方应用时有效，否则为空）。
	UnionOpenID string
	// IsYou / Scope 仅 Mentions 数组元素有意义：is_you 是否 @ 的当前 bot；
	// scope=all=@全体成员 / single=指定 @ 对象（2026-11-18 文档确认）。
	IsYou bool
	Scope string
}

// Part 是消息的一段内容。
type Part struct {
	Kind PartKind
	Text string
	URL  string
	MIME string
	// Width/Height 是图片尺寸（仅图片附件，官方字段）。
	Width  int
	Height int
	// WavURL 是语音消息转好的 WAV 直链（仅语音附件，官方 voice_wav_url）。
	WavURL string
	// ASRText 是语音消息的官方 ASR 参考结果（仅语音附件，可能为空，官方 asr_refer_text）。
	ASRText string
	// Filename/Size 是文件附件的原始文件名与字节数（仅文件附件，官方 filename/size）。
	Filename string
	Size     int64
}

// Event 是归一化后的入站事件。
type Event struct {
	// ID 是平台事件外层信封 id（幂等键，也是 event_id 被动回复锚点）。
	ID       string
	Type     Type
	Scene    Scene
	TargetID string // group_openid / user_openid
	// GroupID 是群事件体里的 group_id（2026-11-18 真机确认与 group_openid 并列下发，
	// 值相同；文档未列）。
	//
	// Deprecated: 与 TargetID 同值，容易误当作"真实群号"拼进接口路径；请用 TargetID。
	GroupID string
	// UnionGroupOpenID 是跨应用关联群 openid（群消息、进退群、群成员增减等群事件下发，
	// 绑定三方应用时有效，否则为空）。
	UnionGroupOpenID string
	Sender           Sender
	// MsgID 是消息事件的被动回复锚点（事件体 d.id，仅 *_MESSAGE_CREATE 有值）。
	MsgID string
	// EventID 是 event_id 被动回复锚点（恒等于外层 ID），仅平台支持 event_id 回复的事件非空。
	EventID   string
	AtMe      bool
	Parts     []Part
	RefIdx    string // 引用回复用：MessageScene.ext 的 msg_idx
	RefMsgIdx string // 本条消息所引用的消息索引（ref_msg_idx）
	// SceneSource 是 message_scene.source（default=默认聊天窗口；2026-11-18 文档确认）。
	SceneSource string
	// Deprecated: 与 Timestamp 同值，仅为兼容旧调用方保留；新代码请用 Timestamp。
	ReceivedAt time.Time
	// Timestamp 是平台事件时间。
	Timestamp time.Time

	// MessageType 是消息内容类型：0=文本 3=卡片 101=并行 102=聊天记录 103=引用。
	MessageType int

	// InteractionType 是互动事件类型（11=按钮 12=快捷菜单需回应；13/14/15/16/18/19/20 无需回应）。
	InteractionType int

	// Interaction 是 INTERACTION_CREATE 的 data.resolved；非互动事件为 nil。
	Interaction *InteractionData

	// InteractionID 是裸 interaction_id（回应互动专用），不能当被动回复的 event_id。
	InteractionID string

	// InteractionUserID 是互动事件的操作人 openid（事件体无 author，权限白名单必须用它）。
	InteractionUserID string

	// Mentions 是消息中 @ 的用户列表（仅消息类事件；可含机器人自身，
	// 元素的 IsYou 标记是否 @ 自己）。
	Mentions []Sender

	// Ark 是结构化卡片数据（官方 ark_data，message_type=3 时有值）。
	Ark *ArkData

	// Elements 是消息元素列表（引用消息时含被引用内容，可递归嵌套）。
	Elements []MessageElement

	// JoinRequest 仅 GROUP_JOIN_REQUEST（JoinRequestID 是审批接口必填参数）。
	JoinRequest *JoinRequest

	// SubscribeResults 仅 SUBSCRIBE_MESSAGE_STATUS：每个订阅模板的授权结果。
	SubscribeResults []SubscribeResult

	// FriendScene / SceneParam 仅 FRIEND_ADD：添加来源场景值与分享链接 callback_data 透传值。
	FriendScene string
	SceneParam  string
	// FriendShortCode 仅 FRIEND_ADD：机器人分享链接的短链 code（2026-11-18 文档确认）。
	FriendShortCode string

	// ChatType / ApplicationID 仅 INTERACTION_CREATE：
	// chat_type 0=频道 1=群聊 2=单聊；application_id 为机器人 AppID（2026-11-18 文档确认）。
	ChatType      int
	ApplicationID string

	// GroupMemberOpenID 仅群场景互动事件。
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
	// Switch 仅授权开启的帧携带（2026-11-18 真机确认）：true=授权已开启；
	// nil=该帧未携带（如首次弹窗授权确认帧）。
	Switch *bool
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
	// RiskTips / UnionOpenID / ApplicantBot 是 2026-11-18 文档确认的顶层字段：
	// risk_tips=安全提示语（可疑申请提示）；union_openid=申请人在应用下的统一标识；
	// bot=申请人是否机器人账号（官方语义标注含糊，不建议依赖）。
	RiskTips     string
	UnionOpenID  string
	ApplicantBot bool
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
