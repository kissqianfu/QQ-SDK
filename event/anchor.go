package event

// anchor.go 回答一个问题：**这条事件能用什么 id 做被动回复锚点？**
//
// 背景（真实线上事故）：平台有两个完全不同的 id 都叫 "id"——
//   - 事件外层信封 id（ws 网关帧 / webhook 信封的 id，形如 INTERACTION_CREATE:xxx）：
//     发送接口的 event_id 用它；
//   - 事件体 d.id：消息事件里是 msg_id；互动事件里是裸 interaction_id，
//     只能用于 PUT /interactions/{id}，**不能**当 event_id 用（实测 40034025）。
//
// 之前把两者混在一个 Event.EventID 字段里，导致群聊按钮回调的被动回复必失败。
// 现在语义拆开：Event.ID == Event.EventID == 外层 id；Event.InteractionID == 裸 interaction_id。
// 本文件把"哪些事件支持哪种锚点"收敛成纯函数，SDK 与宿主都从这里取，不再各写一份。

// AnchorKind 是锚点类型。
type AnchorKind uint8

const (
	// AnchorMsgID 用 msg_id 锚点（send.Message.ReplyTo）。
	AnchorMsgID AnchorKind = iota + 1
	// AnchorEventID 用 event_id 锚点（send.Message.ReplyToEvent）。
	AnchorEventID
)

// Anchor 是一个被动回复锚点候选。
type Anchor struct {
	Kind AnchorKind
	ID   string
}

// String 便于日志/测试输出。
func (a Anchor) String() string {
	if a.Kind == AnchorMsgID {
		return "msg_id:" + a.ID
	}
	return "event_id:" + a.ID
}

// IsMessageType 报告该事件是否是"用户消息"事件（正文在 Parts 里，回复用 msg_id）。
func IsMessageType(t Type) bool {
	switch t {
	case C2CMessageCreate, GroupAtMessageCreate, GroupMessageCreate:
		return true
	}
	return false
}

// SupportsEventIDReply 报告平台是否允许用 event_id 回复该事件。
//
// 口径来自官方发送接口文档：
//   - 群聊：INTERACTION_CREATE / GROUP_ADD_ROBOT / GROUP_MSG_RECEIVE；
//   - 单聊：INTERACTION_CREATE / C2C_MSG_RECEIVE / FRIEND_ADD。
func SupportsEventIDReply(t Type) bool {
	switch t {
	case InteractionCreate, GroupAddRobot, GroupMsgReceive, C2CMsgReceive, FriendAdd:
		return true
	}
	return false
}

// ReplyAnchors 返回该事件的被动回复候选锚点（按优先级，过滤空值、按类型+值去重）。
//
// 规则：
//   - 消息事件：msg_id（d.id）。消息事件不支持 event_id 回复，不要构造它；
//   - 互动事件：event_id（外层 id）→ msg_id（resolved.message_id，按钮所在消息）；
//   - 其它支持 event_id 的事件：event_id。
//
// 注意：这里只给候选，不判断"平台是否接受"——那需要真实请求与错误码降级
// （api.Client.SendReply 负责）。
func (e *Event) ReplyAnchors() []Anchor {
	if e == nil {
		return nil
	}
	var out []Anchor
	seen := make(map[Anchor]struct{}, 3)
	add := func(kind AnchorKind, id string) {
		if id == "" {
			return
		}
		a := Anchor{Kind: kind, ID: id}
		if _, ok := seen[a]; ok {
			return
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}

	switch {
	case IsMessageType(e.Type):
		add(AnchorMsgID, e.MsgID)
	case e.Type == InteractionCreate:
		add(AnchorEventID, e.EventID)
		if e.Interaction != nil {
			add(AnchorMsgID, e.Interaction.MessageID)
		}
	case SupportsEventIDReply(e.Type):
		add(AnchorEventID, e.EventID)
	}
	return out
}
