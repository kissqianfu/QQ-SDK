package event

// anchor.go 回答"这条事件能用什么 id 做被动回复锚点"。
// Event.ID == Event.EventID == 外层信封 id（event_id 回复用）；
// d.id 在消息事件里是 msg_id，互动事件里是裸 interaction_id（不能当 event_id）。

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

// SupportsEventIDReply 报告平台是否允许用 event_id 回复该事件（口径来自官方发送接口文档）。
func SupportsEventIDReply(t Type) bool {
	switch t {
	case InteractionCreate, GroupAddRobot, GroupMsgReceive, C2CMsgReceive, FriendAdd:
		return true
	}
	return false
}

// ReplyAnchors 返回该事件的被动回复候选锚点（按优先级、去重；只给候选，不判断平台是否接受）。
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
