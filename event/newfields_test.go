package event

// newfields_test.go 覆盖 2026-08/09/10 文档新增的事件字段：
// GROUP_JOIN_REQUEST、INTERACTION_CREATE.data.resolved、SUBSCRIBE_MESSAGE_STATUS.result、
// FRIEND_ADD 的 scene/scene_param、GROUP_MEMBER_ADD/REMOVE 的 member_openid、Event.Timestamp。

import "testing"

func TestParseGroupJoinRequest(t *testing.T) {
	raw := `{"id":"EV-J1","op":0,"t":"GROUP_JOIN_REQUEST","d":{
		"group_openid":"g1","join_request_id":"jr-1","member_openid":"m1","username":"张三",
		"apply_at":"2026-08-10T10:00:00+08:00","apply_source":"invited","invited_by":"u9",
		"verify_info":{"method":"qa","verify_message":"请答","review_qa_list":[{"question":"1+1","answer":"2"}]},
		"auto_approved":{"strategy_id":"s1"}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.Type != GroupJoinRequest || ev.Scene != SceneGroup || ev.TargetID != "g1" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Sender.ID != "m1" {
		t.Fatalf("Sender.ID = %q, want m1（member_openid）", ev.Sender.ID)
	}
	jr := ev.JoinRequest
	if jr == nil {
		t.Fatal("JoinRequest = nil")
	}
	if jr.JoinRequestID != "jr-1" || jr.ApplySource != JoinApplyInvited || jr.InvitedBy != "u9" {
		t.Fatalf("join request = %+v", jr)
	}
	if jr.VerifyInfo == nil || jr.VerifyInfo.Method != "qa" || len(jr.VerifyInfo.ReviewQAList) != 1 {
		t.Fatalf("verify info = %+v", jr.VerifyInfo)
	}
	if jr.AutoApprovedStrategyID != "s1" {
		t.Fatalf("auto approved = %q", jr.AutoApprovedStrategyID)
	}
}

func TestParseInteractionResolved(t *testing.T) {
	raw := `{"id":"EV-I1","op":0,"t":"INTERACTION_CREATE","d":{
		"id":"inter-1","type":13,"scene":"c2c","user_openid":"u1",
		"timestamp":"2026-07-20T21:53:54+08:00",
		"data":{"type":13,"resolved":{"button_id":"b1","button_data":"d1","message_id":"m1",
			"feedback_opt":"LIKE","checked":1,"action":"ENTER_STORY",
			"message_scene":{"ext":["disable_net_search=1"]},
			"authorize_data":{"opt_scene":"setting","scope":"c2c_push"}}}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.InteractionType != 13 || ev.Scene != SceneC2C || ev.TargetID != "u1" {
		t.Fatalf("event = %+v", ev)
	}
	in := ev.Interaction
	if in == nil {
		t.Fatal("Interaction = nil")
	}
	if in.ButtonID != "b1" || in.ButtonData != "d1" || in.MessageID != "m1" {
		t.Fatalf("interaction = %+v", in)
	}
	if in.FeedbackOpt != "LIKE" || !in.Checked || in.Action != "ENTER_STORY" {
		t.Fatalf("interaction = %+v", in)
	}
	if len(in.MessageSceneExt) != 1 || in.MessageSceneExt[0] != "disable_net_search=1" {
		t.Fatalf("message_scene.ext = %v", in.MessageSceneExt)
	}
	if in.AuthorizeData == nil || in.AuthorizeData.OptScene != "setting" || in.AuthorizeData.Scope != "c2c_push" {
		t.Fatalf("authorize_data = %+v", in.AuthorizeData)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("Timestamp 未解析")
	}
}

func TestParseSubscribeMessageStatus(t *testing.T) {
	raw := `{"id":"EV-S1","op":0,"t":"SUBSCRIBE_MESSAGE_STATUS","d":{
		"openid":"u1","result":[
			{"template_id":10001,"custom_template_id":"tpl_a","op":1,"subscribe_id":"sub_1","subscribe_ts":10,"update_ts":11},
			{"template_id":10002,"custom_template_id":"tpl_b","op":2,"subscribe_id":"sub_2","subscribe_ts":12,"update_ts":13}]}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(ev.SubscribeResults) != 2 {
		t.Fatalf("results = %+v", ev.SubscribeResults)
	}
	first := ev.SubscribeResults[0]
	if first.TemplateID != 10001 || first.CustomTemplateID != "tpl_a" || first.Op != 1 || first.SubscribeID != "sub_1" {
		t.Fatalf("first = %+v", first)
	}
	if ev.TargetID != "u1" {
		t.Fatalf("TargetID = %q", ev.TargetID)
	}
}

func TestParseFriendAddSceneAndParam(t *testing.T) {
	raw := `{"id":"EV-F1","op":0,"t":"FRIEND_ADD","d":{
		"openid":"u2","timestamp":1700000000,"scene":1001,"scene_param":"campaign-1"}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.FriendScene != "1001" || ev.SceneParam != "campaign-1" {
		t.Fatalf("friend scene/param = %q/%q", ev.FriendScene, ev.SceneParam)
	}
	if ev.EventID != "EV-F1" || ev.Scene != SceneC2C {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseGroupMemberSender(t *testing.T) {
	cases := []struct{ name, raw, wantSender string }{
		{
			name:       "GROUP_MEMBER_ADD",
			raw:        `{"id":"EV-M1","op":0,"t":"GROUP_MEMBER_ADD","d":{"group_openid":"g1","member_openid":"m1","timestamp":1700000000}}`,
			wantSender: "m1",
		},
		{
			name:       "GROUP_MEMBER_REMOVE",
			raw:        `{"id":"EV-M2","op":0,"t":"GROUP_MEMBER_REMOVE","d":{"group_openid":"g1","member_openid":"m2","user_openid":"u2"}}`,
			wantSender: "m2",
		},
		{
			name:       "GROUP_MSG_REJECT",
			raw:        `{"id":"EV-R1","op":0,"t":"GROUP_MSG_REJECT","d":{"group_openid":"g1","op_member_openid":"m3"}}`,
			wantSender: "m3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if ev.Sender.ID != tc.wantSender {
				t.Fatalf("Sender.ID = %q, want %q", ev.Sender.ID, tc.wantSender)
			}
			if ev.TargetID != "g1" {
				t.Fatalf("TargetID = %q, want g1", ev.TargetID)
			}
		})
	}
}
