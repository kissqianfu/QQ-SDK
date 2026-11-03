package event

// anchors_extra_test.go 锁定"被动回复 event_id 锚点"的支持事件集合：
// 官方单聊消息文档明确 event_id 支持 INTERACTION_CREATE / C2C_MSG_RECEIVE / FRIEND_ADD，
// 群聊为 INTERACTION_CREATE / GROUP_ADD_ROBOT / GROUP_MSG_RECEIVE。
// 之前 C2C_MSG_RECEIVE 与 FRIEND_ADD 没解析锚点，业务调 ReplyToEvent(ev.ID) 会静默变成主动消息。

import "testing"

func TestParseC2CMsgReceiveSetsEventID(t *testing.T) {
	raw := `{"id":"EV-C2C-1","op":0,"t":"C2C_MSG_RECEIVE","d":{"user_openid":"u1","openid":"u1","timestamp":"2026-07-21T10:30:00+08:00"}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.EventID != "EV-C2C-1" {
		t.Fatalf("EventID = %q, want EV-C2C-1", ev.EventID)
	}
	if ev.Scene != SceneC2C || ev.TargetID != "u1" {
		t.Fatalf("scene/target = %v/%q", ev.Scene, ev.TargetID)
	}
}

func TestParseFriendAddSetsEventID(t *testing.T) {
	raw := `{"id":"EV-FRIEND-1","op":0,"t":"FRIEND_ADD","d":{"openid":"u2","timestamp":1700000000}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.EventID != "EV-FRIEND-1" {
		t.Fatalf("EventID = %q, want EV-FRIEND-1", ev.EventID)
	}
	if ev.Scene != SceneC2C || ev.TargetID != "u2" {
		t.Fatalf("scene/target = %v/%q", ev.Scene, ev.TargetID)
	}
}

func TestParseGroupMsgReceiveKeepsEventID(t *testing.T) {
	raw := `{"id":"EV-G-1","op":0,"t":"GROUP_MSG_RECEIVE","d":{"group_openid":"g1","member_openid":"m1"}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.EventID != "EV-G-1" || ev.Scene != SceneGroup || ev.TargetID != "g1" {
		t.Fatalf("event = %+v", ev)
	}
}
