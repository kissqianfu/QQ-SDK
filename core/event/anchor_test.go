package event

// anchor_test.go 锁定"哪类事件能用哪种锚点"的协议知识：
// 这类规则放 SDK 才可以被所有宿主复用，避免各宿主各写一份（并各踩一次 40034025）。

import "testing"

func TestIsMessageTypeAndEventIDReplySupport(t *testing.T) {
	msg := []Type{C2CMessageCreate, GroupAtMessageCreate, GroupMessageCreate}
	for _, typ := range msg {
		if !IsMessageType(typ) {
			t.Fatalf("%s 应判为消息事件", typ)
		}
	}
	for _, typ := range []Type{InteractionCreate, GroupAddRobot, GroupMsgReceive, C2CMsgReceive, FriendAdd} {
		if !SupportsEventIDReply(typ) {
			t.Fatalf("%s 应支持 event_id 回复", typ)
		}
		if IsMessageType(typ) {
			t.Fatalf("%s 不应判为消息事件", typ)
		}
	}
	if SupportsEventIDReply(GroupMessageCreate) {
		t.Fatal("普通群消息不支持 event_id 回复")
	}
}

func TestReplyAnchorsPerEventType(t *testing.T) {
	// 消息事件：只有 msg_id。
	msg := &Event{Type: GroupAtMessageCreate, MsgID: "m1", EventID: "EV1"}
	if got := msg.ReplyAnchors(); len(got) != 1 || got[0] != (Anchor{AnchorMsgID, "m1"}) {
		t.Fatalf("message anchors = %v", got)
	}

	// 互动事件：外层 event_id 优先，按钮消息 msg_id 兜底。
	in := &Event{
		Type:    InteractionCreate,
		EventID: "INTERACTION_CREATE:ev1",
		Interaction: &InteractionData{
			MessageID: "btn-msg",
		},
	}
	got := in.ReplyAnchors()
	want := []Anchor{
		{AnchorEventID, "INTERACTION_CREATE:ev1"},
		{AnchorMsgID, "btn-msg"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("interaction anchors = %v, want %v", got, want)
	}

	// 互动事件没有按钮消息时只剩 event_id。
	onlyEvent := &Event{Type: InteractionCreate, EventID: "EV2"}
	if got := onlyEvent.ReplyAnchors(); len(got) != 1 || got[0].Kind != AnchorEventID {
		t.Fatalf("interaction(no msg) anchors = %v", got)
	}

	// 其它支持 event_id 的事件。
	for _, typ := range []Type{C2CMsgReceive, GroupMsgReceive, GroupAddRobot, FriendAdd} {
		ev := &Event{Type: typ, EventID: "EV3"}
		got := ev.ReplyAnchors()
		if len(got) != 1 || got[0] != (Anchor{AnchorEventID, "EV3"}) {
			t.Fatalf("%s anchors = %v", typ, got)
		}
	}

	// 不支持锚点的事件 / 空 id：返回空。
	if got := (&Event{Type: "UNKNOWN", MsgID: "x"}).ReplyAnchors(); len(got) != 0 {
		t.Fatalf("unknown anchors = %v", got)
	}
	if got := (&Event{Type: GroupAtMessageCreate}).ReplyAnchors(); len(got) != 0 {
		t.Fatalf("empty anchors = %v", got)
	}
	var nilEvent *Event
	if got := nilEvent.ReplyAnchors(); len(got) != 0 {
		t.Fatalf("nil event anchors = %v", got)
	}
}

func TestAnchorString(t *testing.T) {
	if got := (Anchor{AnchorMsgID, "m"}).String(); got != "msg_id:m" {
		t.Fatalf("String = %q", got)
	}
	if got := (Anchor{AnchorEventID, "e"}).String(); got != "event_id:e" {
		t.Fatalf("String = %q", got)
	}
}
