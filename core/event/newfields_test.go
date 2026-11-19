package event

// newfields_test.go 覆盖 2026-08/09/10 文档新增的事件字段（入群申请/互动 resolved/订阅状态等）。

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

// TestParseInteractionSenderC2C 锁定评审发现的缺陷：INTERACTION_CREATE 没有 author，
// 单聊的操作人只在 d.user_openid 里；Sender.ID / InteractionUserID 必须能拿到，
// 否则按 Sender.ID 做按钮权限白名单时单聊场景恒为空串（越权/全拒）。
func TestParseInteractionSenderC2C(t *testing.T) {
	raw := `{"id":"EV-I2","op":0,"t":"INTERACTION_CREATE","d":{
		"id":"inter-2","type":11,"scene":"c2c","user_openid":"u-c2c",
		"data":{"type":11,"resolved":{"button_id":"b1","button_data":"a|act=ack"}}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.Sender.ID != "u-c2c" {
		t.Fatalf("C2C Sender.ID = %q, want u-c2c", ev.Sender.ID)
	}
	if ev.InteractionUserID != "u-c2c" {
		t.Fatalf("InteractionUserID = %q, want u-c2c", ev.InteractionUserID)
	}
	if ev.TargetID != "u-c2c" {
		t.Fatalf("TargetID = %q", ev.TargetID)
	}
	if ev.Interaction == nil || ev.Interaction.ButtonData != "a|act=ack" {
		t.Fatalf("Interaction = %+v", ev.Interaction)
	}

	// 群场景：操作人取 group_member_openid。
	grp := `{"id":"EV-I3","op":0,"t":"INTERACTION_CREATE","d":{
		"id":"inter-3","type":11,"scene":"group","group_openid":"g1","group_member_openid":"m-1",
		"data":{"type":11,"resolved":{"button_data":"a|act=mute1h"}}}}`
	ev2, err := Parse([]byte(grp))
	if err != nil {
		t.Fatalf("Parse(group) error = %v", err)
	}
	if ev2.Sender.ID != "m-1" || ev2.InteractionUserID != "m-1" || ev2.GroupMemberOpenID != "m-1" {
		t.Fatalf("group sender = %q/%q/%q", ev2.Sender.ID, ev2.InteractionUserID, ev2.GroupMemberOpenID)
	}
	// resolved.user_id 优先（频道场景才有值，这里只验证优先级）。
	withUID := `{"id":"EV-I4","op":0,"t":"INTERACTION_CREATE","d":{
		"id":"inter-4","type":11,"scene":"group","group_openid":"g1","group_member_openid":"m-1",
		"data":{"type":11,"resolved":{"user_id":"uid-x"}}}}`
	ev3, err := Parse([]byte(withUID))
	if err != nil {
		t.Fatalf("Parse(uid) error = %v", err)
	}
	if ev3.InteractionUserID != "uid-x" {
		t.Fatalf("InteractionUserID = %q, want uid-x（resolved.user_id 优先）", ev3.InteractionUserID)
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

// TestParseNov16SenderProfile 覆盖 2026-11-16 文档新增字段：
// 单聊/群聊 author.avatar（头像）、群聊 author.member_nickname（群内昵称）、
// 群事件顶层 union_group_openid（跨应用关联群标识）、author.union_openid。
func TestParseNov16SenderProfile(t *testing.T) {
	// 群消息：member_nickname + avatar + union_group_openid。
	grp := `{"id":"EV-N1","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{
		"id":"msg-1","content":"早上好","group_openid":"g1","union_group_openid":"ug1",
		"author":{"id":"a1","member_openid":"m1","username":"小明","avatar":"https://avatar.example/g1m1.png",
			"member_nickname":"群里的小明","union_openid":"uo1","bot":false,"member_role":"member"},
		"timestamp":"2026-11-16T09:00:00+08:00"}}`
	ev, err := Parse([]byte(grp))
	if err != nil {
		t.Fatalf("Parse(group) error = %v", err)
	}
	if ev.Sender.Avatar != "https://avatar.example/g1m1.png" {
		t.Fatalf("Sender.Avatar = %q", ev.Sender.Avatar)
	}
	if ev.Sender.MemberNickname != "群里的小明" {
		t.Fatalf("Sender.MemberNickname = %q", ev.Sender.MemberNickname)
	}
	if ev.Sender.UnionOpenID != "uo1" {
		t.Fatalf("Sender.UnionOpenID = %q", ev.Sender.UnionOpenID)
	}
	if ev.UnionGroupOpenID != "ug1" {
		t.Fatalf("UnionGroupOpenID = %q, want ug1", ev.UnionGroupOpenID)
	}

	// 单聊消息：avatar + union_openid，无 member_nickname（应为空）。
	c2c := `{"id":"EV-N2","op":0,"t":"C2C_MESSAGE_CREATE","d":{
		"id":"msg-2","content":"hi","user_openid":"u1",
		"author":{"id":"a2","user_openid":"u1","username":"小刚","avatar":"https://avatar.example/u1.png",
			"union_openid":"uo2","bot":false},
		"timestamp":"2026-11-16T09:01:00+08:00"}}`
	ev2, err := Parse([]byte(c2c))
	if err != nil {
		t.Fatalf("Parse(c2c) error = %v", err)
	}
	if ev2.Sender.Avatar != "https://avatar.example/u1.png" || ev2.Sender.UnionOpenID != "uo2" {
		t.Fatalf("c2c sender = %+v", ev2.Sender)
	}
	if ev2.Sender.MemberNickname != "" || ev2.UnionGroupOpenID != "" {
		t.Fatalf("c2c 不应携带群字段: nickname=%q union_group=%q",
			ev2.Sender.MemberNickname, ev2.UnionGroupOpenID)
	}

	// 无新字段的旧事件：零值兼容（追加字段不破坏旧解析）。
	old := `{"id":"EV-N3","op":0,"t":"GROUP_MEMBER_ADD","d":{"group_openid":"g1","member_openid":"m1"}}`
	ev3, err := Parse([]byte(old))
	if err != nil {
		t.Fatalf("Parse(old) error = %v", err)
	}
	if ev3.Sender.Avatar != "" || ev3.Sender.MemberNickname != "" || ev3.UnionGroupOpenID != "" {
		t.Fatalf("旧事件新字段应为零值: %+v", ev3)
	}
}

// TestParseNov18DocFields 覆盖 2026-11-18 文档核对发现的未建模字段：
// mentions[].is_you / mentions[].scope / message_scene.source（消息类）、
// INTERACTION_CREATE 的 chat_type / application_id、FRIEND_ADD 的 short_code、
// GROUP_JOIN_REQUEST 的 risk_tips / union_openid / bot。
func TestParseNov18DocFields(t *testing.T) {
	// 群消息：mentions（is_you + scope）+ message_scene.source。
	grp := `{"id":"EV-N18a","op":0,"t":"GROUP_MESSAGE_CREATE","d":{
		"id":"msg-18","content":" <@m9> 在吗","group_openid":"g1",
		"message_scene":{"source":"default","ext":["msg_idx=REFIDX_x"]},
		"mentions":[{"id":"a9","member_openid":"m9","username":"别人","bot":false,"is_you":true,"scope":"single","member_nickname":"被@的人"},
			{"id":"a8","member_openid":"m8","bot":false,"is_you":false,"scope":"all","member_nickname":"路人"}],
		"author":{"id":"a1","member_openid":"m1","username":"小明","bot":false}}}`
	ev, err := Parse([]byte(grp))
	if err != nil {
		t.Fatalf("Parse(group) error = %v", err)
	}
	if ev.SceneSource != "default" {
		t.Fatalf("SceneSource = %q, want default", ev.SceneSource)
	}
	if len(ev.Mentions) != 2 {
		t.Fatalf("Mentions 数量 = %d", len(ev.Mentions))
	}
	if !ev.Mentions[0].IsYou || ev.Mentions[0].Scope != "single" || ev.Mentions[0].MemberNickname != "被@的人" {
		t.Fatalf("mentions[0] = %+v", ev.Mentions[0])
	}
	if ev.Mentions[1].IsYou || ev.Mentions[1].Scope != "all" {
		t.Fatalf("mentions[1] = %+v", ev.Mentions[1])
	}

	// 互动事件：chat_type / application_id（version 仅建模不透出，这里不再断言）。
	inter := `{"id":"EV-N18b","op":0,"t":"INTERACTION_CREATE","d":{
		"id":"inter-18","type":11,"scene":"group","chat_type":1,"version":1,"application_id":"102083127",
		"group_openid":"g1","group_member_openid":"m-1",
		"data":{"type":11,"resolved":{"button_data":"a|act=ok"}}}}`
	ev2, err := Parse([]byte(inter))
	if err != nil {
		t.Fatalf("Parse(interaction) error = %v", err)
	}
	if ev2.ChatType != 1 || ev2.ApplicationID != "102083127" {
		t.Fatalf("ChatType/ApplicationID = %d/%q", ev2.ChatType, ev2.ApplicationID)
	}

	// FRIEND_ADD：short_code。
	friend := `{"id":"EV-N18c","op":0,"t":"FRIEND_ADD","d":{
		"openid":"u2","timestamp":1700000000,"scene":2003,"scene_param":"cb-1","short_code":"AbCdEf"}}`
	ev3, err := Parse([]byte(friend))
	if err != nil {
		t.Fatalf("Parse(friend) error = %v", err)
	}
	if ev3.FriendShortCode != "AbCdEf" {
		t.Fatalf("FriendShortCode = %q", ev3.FriendShortCode)
	}

	// GROUP_JOIN_REQUEST：risk_tips / union_openid / bot。
	jrRaw := `{"id":"EV-N18d","op":0,"t":"GROUP_JOIN_REQUEST","d":{
		"group_openid":"g1","join_request_id":"jr-18","member_openid":"m1","username":"张三",
		"risk_tips":"该申请存在风险","union_openid":"uo-18","bot":false,
		"apply_at":"2026-11-18T10:00:00+08:00","apply_source":"self_apply"}}`
	ev4, err := Parse([]byte(jrRaw))
	if err != nil {
		t.Fatalf("Parse(join) error = %v", err)
	}
	if ev4.JoinRequest == nil || ev4.JoinRequest.RiskTips != "该申请存在风险" ||
		ev4.JoinRequest.UnionOpenID != "uo-18" || ev4.JoinRequest.ApplicantBot {
		t.Fatalf("JoinRequest = %+v", ev4.JoinRequest)
	}
}

// TestParseNov18ListenerFields 覆盖 2026-11-18 QQ-Listener 真机监听发现的新字段：
// 群消息体 group_id（与 group_openid 并列，文档未列）、
// 互动 type=18 authorize_data.switch（仅授权开启帧携带）。
func TestParseNov18ListenerFields(t *testing.T) {
	grp := `{"id":"EV-L18a","op":0,"t":"GROUP_MESSAGE_CREATE","d":{
		"id":"msg-l1","content":"你好","group_id":"C7613C28AF64B2246F0A588F8926B9B0",
		"group_openid":"C7613C28AF64B2246F0A588F8926B9B0",
		"message_scene":{"source":"default","ext":["msg_idx=REFIDX_x"]},
		"author":{"id":"a1","member_openid":"m1","member_role":"owner","username":"- -","bot":false}}}`
	ev, err := Parse([]byte(grp))
	if err != nil {
		t.Fatalf("Parse(group) error = %v", err)
	}
	if ev.GroupID != "C7613C28AF64B2246F0A588F8926B9B0" {
		t.Fatalf("GroupID = %q", ev.GroupID)
	}
	if ev.TargetID != ev.GroupID || ev.Sender.MemberRole != "owner" {
		t.Fatalf("TargetID/Sender.MemberRole = %q/%q", ev.TargetID, ev.Sender.MemberRole)
	}

	inter := `{"id":"EV-L18b","op":0,"t":"INTERACTION_CREATE","d":{
		"application_id":"1905862011","type":18,"scene":"c2c","version":1,
		"data":{"resolved":{"authorize_data":{"opt_scene":"setting","scope":"c2c_push","switch":true}}},
		"user_openid":"u1","timestamp":"2026-11-18T22:19:48+08:00"}}`
	ev2, err := Parse([]byte(inter))
	if err != nil {
		t.Fatalf("Parse(interaction) error = %v", err)
	}
	ad := ev2.Interaction.AuthorizeData
	if ad == nil || ad.Scope != "c2c_push" || ad.OptScene != "setting" {
		t.Fatalf("AuthorizeData = %+v", ad)
	}
	if ad.Switch == nil || !*ad.Switch {
		t.Fatalf("AuthorizeData.Switch = %v, want true", ad.Switch)
	}

	// 首次授权帧不带 switch：Switch 应为 nil 而不是 false。
	inter2 := `{"id":"EV-L18c","op":0,"t":"INTERACTION_CREATE","d":{
		"application_id":"1905862011","type":18,"scene":"c2c","version":1,
		"data":{"resolved":{"authorize_data":{"opt_scene":"setting","scope":"c2c_push"}}},"user_openid":"u1"}}`
	ev3, err := Parse([]byte(inter2))
	if err != nil {
		t.Fatalf("Parse(interaction2) error = %v", err)
	}
	if ad := ev3.Interaction.AuthorizeData; ad == nil || ad.Switch != nil {
		t.Fatalf("AuthorizeData.Switch = %v, want nil", ad)
	}
}
