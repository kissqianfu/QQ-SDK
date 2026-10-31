package event

import (
	"strings"
	"testing"
	"time"
)

func TestParseGroupAtMessage(t *testing.T) {
	raw := `{
		"id": "EV-1",
		"op": 0,
		"t": "GROUP_AT_MESSAGE_CREATE",
		"s": 42,
		"d": {
			"id": "msg-1",
			"content": " 你好 ",
			"group_openid": "g-1",
			"timestamp": "2026-10-31T08:00:00+08:00",
			"author": {"id": "A1", "member_openid": "m-1", "username": "小明", "bot": false, "member_role": "admin"},
			"attachments": [{"content_type": "image/png", "url": "https://example.com/a.png", "filename": "a.png", "size": 123}],
			"message_scene": {"ext": [{"msg_idx": "REFIDX_1"}]}
		}
	}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.ID != "EV-1" || ev.Type != GroupAtMessageCreate || ev.Scene != SceneGroup {
		t.Fatalf("event header = %+v", ev)
	}
	if ev.TargetID != "g-1" || ev.MsgID != "msg-1" || !ev.AtMe {
		t.Fatalf("target/msg/atme = %q/%q/%v", ev.TargetID, ev.MsgID, ev.AtMe)
	}
	if ev.Sender.ID != "m-1" || ev.Sender.Name != "小明" || ev.Sender.MemberRole != "admin" || ev.Sender.Bot {
		t.Fatalf("sender = %+v", ev.Sender)
	}
	if len(ev.Parts) != 2 || ev.Parts[0].Kind != PartText || ev.Parts[1].Kind != PartImage {
		t.Fatalf("parts = %+v", ev.Parts)
	}
	if ev.RefIdx != "REFIDX_1" {
		t.Fatalf("refIdx = %q", ev.RefIdx)
	}
	want := time.Date(2026, 10, 31, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if !ev.ReceivedAt.Equal(want) {
		t.Fatalf("ReceivedAt = %v, want %v", ev.ReceivedAt, want)
	}
	if string(ev.Raw) != raw {
		t.Fatal("Raw payload not preserved")
	}
}

func TestParseC2C(t *testing.T) {
	raw := `{"id":"EV-2","op":0,"t":"C2C_MESSAGE_CREATE","d":{
		"id":"msg-2","content":"hi","timestamp":"2026-10-31T00:00:00Z",
		"author":{"id":"X","user_openid":"u-1","username":"我","bot":false}}}`

	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.Scene != SceneC2C || ev.TargetID != "u-1" || ev.Sender.ID != "u-1" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.AtMe {
		t.Fatal("c2c should not be AtMe")
	}
}

func TestParseInteractionSetsEventID(t *testing.T) {
	raw := `{"id":"EV-3","op":0,"t":"INTERACTION_CREATE","d":{"id":"inter-1","group_openid":"g-1","author":{"member_openid":"m-1"}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.EventID != "inter-1" || ev.Scene != SceneGroup {
		t.Fatalf("event = %+v", ev)
	}
}

// 官方注明 interaction_id 不带 "INTERACTION_CREATE:" 前缀；历史事件出现过带前缀形态，须剥掉。
func TestParseInteractionStripsEventIDPrefix(t *testing.T) {
	raw := `{"id":"EV-4","op":0,"t":"INTERACTION_CREATE","d":{"id":"INTERACTION_CREATE:inter-9","scene":"c2c","user_openid":"u-1","type":11}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.EventID != "inter-9" {
		t.Fatalf("EventID = %q, want inter-9", ev.EventID)
	}
	if ev.Scene != SceneC2C {
		t.Fatalf("Scene = %v, want c2c", ev.Scene)
	}
}

// 官方 autogen 示例：message_scene.ext 是 "key=value" 字符串数组，msg_idx 才是引用索引。
func TestParseGroupRefIdxKeyValue(t *testing.T) {
	raw := `{"id":"EV-5","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{
		"id":"ROBOT1.0_msg1","content":" 看图 ","group_openid":"g-1","message_type":0,
		"timestamp":"2026-07-21T10:05:00+08:00",
		"author":{"id":"m-1","member_openid":"m-1","username":"小红","bot":false},
		"message_scene":{"source":"default","ext":["msg_idx=REFIDX_yyy==","auth_token=tok","ref_msg_idx=TMP_ref1"]},
		"attachments":[{"content_type":"image/jpeg","filename":"photo.jpg","url":"https://multimedia.nt.qq.com.cn/x",
			"width":1920,"height":1080,"size":256000}]}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.RefIdx != "REFIDX_yyy==" {
		t.Fatalf("RefIdx = %q, want REFIDX_yyy==（不能带 msg_idx= 前缀）", ev.RefIdx)
	}
	if ev.RefMsgIdx != "TMP_ref1" {
		t.Fatalf("RefMsgIdx = %q, want TMP_ref1", ev.RefMsgIdx)
	}
	if ev.MessageType != 0 || ev.TargetID != "g-1" || ev.MsgID != "ROBOT1.0_msg1" {
		t.Fatalf("event = %+v", ev)
	}
	if len(ev.Parts) != 2 {
		t.Fatalf("Parts = %d, want 2（文本+图片）", len(ev.Parts))
	}
	img := ev.Parts[1]
	if img.Kind != PartImage || img.Width != 1920 || img.Height != 1080 || img.URL == "" {
		t.Fatalf("image part = %+v", img)
	}
}

// 语音附件：voice_wav_url 与 asr_refer_text（官方字段，content_type=voice）。
func TestParseVoiceAttachment(t *testing.T) {
	raw := `{"id":"EV-6","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{
		"id":"ROBOT1.0_msg2","content":"","group_openid":"g-1",
		"author":{"member_openid":"m-2"},
		"attachments":[{"content_type":"voice","url":"https://x/silk","voice_wav_url":"https://x/wav","asr_refer_text":"今天天气怎么样","size":12000}]}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(ev.Parts) != 1 || ev.Parts[0].Kind != PartVoice {
		t.Fatalf("Parts = %+v", ev.Parts)
	}
	if ev.Parts[0].WavURL != "https://x/wav" || ev.Parts[0].ASRText != "今天天气怎么样" {
		t.Fatalf("voice part = %+v", ev.Parts[0])
	}
}

// FRIEND_ADD：官方事件体字段是 openid（不是 user_openid），author 只有 union_openid。
func TestParseFriendAdd(t *testing.T) {
	raw := `{"id":"EV-7","op":0,"t":"FRIEND_ADD","d":{
		"openid":"u-100","timestamp":1784570523,"scene":1001,"scene_param":"",
		"author":{"union_openid":"UNION-1"}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.TargetID != "u-100" || ev.Scene != SceneC2C {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Sender.ID != "u-100" {
		t.Fatalf("Sender = %+v, want openid u-100", ev.Sender)
	}
}

// GROUP_ADD_ROBOT：官方事件体没有 id/author，操作人是 op_member_openid；event_id 取外层 id。
func TestParseGroupAddRobot(t *testing.T) {
	raw := `{"id":"EV-8","op":0,"t":"GROUP_ADD_ROBOT","d":{
		"group_openid":"g-9","op_member_openid":"m-9","timestamp":1784570534}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.TargetID != "g-9" {
		t.Fatalf("TargetID = %q", ev.TargetID)
	}
	if ev.Sender.ID != "m-9" {
		t.Fatalf("Sender = %+v, want op_member_openid m-9", ev.Sender)
	}
	if ev.EventID != "EV-8" {
		t.Fatalf("EventID = %q, want 外层 id EV-8", ev.EventID)
	}
}

func TestParseInteractionSceneAndType(t *testing.T) {
	// 官方 INTERACTION_CREATE 带 d.scene（c2c=单聊, group=群聊）与 d.type（互动类型）。
	group := `{"id":"EV-8","op":0,"t":"INTERACTION_CREATE","d":{"id":"it-g","type":11,"scene":"group","group_openid":"g-9","author":{"member_openid":"m-9"}}}`
	ev, err := Parse([]byte(group))
	if err != nil {
		t.Fatalf("Parse(group) error = %v", err)
	}
	if ev.Scene != SceneGroup || ev.TargetID != "g-9" || ev.EventID != "it-g" || ev.InteractionType != 11 {
		t.Fatalf("group interaction = %+v", ev)
	}

	c2c := `{"id":"EV-9","op":0,"t":"INTERACTION_CREATE","d":{"id":"it-c","type":12,"scene":"c2c","user_openid":"u-9","author":{"user_openid":"u-9"}}}`
	ev, err = Parse([]byte(c2c))
	if err != nil {
		t.Fatalf("Parse(c2c) error = %v", err)
	}
	if ev.Scene != SceneC2C || ev.TargetID != "u-9" || ev.InteractionType != 12 {
		t.Fatalf("c2c interaction = %+v", ev)
	}
}

// 历史 d.id 存在时优先取 d.id（firstNonEmpty），官方现行事件体没有 id 则回退外层 id。
func TestParseGroupAddRobotEventIDFallback(t *testing.T) {
	raw := `{"id":"EV-4","op":0,"t":"GROUP_ADD_ROBOT","d":{"id":"add-1","group_openid":"g-1","op_member_openid":"m-1"}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.EventID != "add-1" || ev.TargetID != "g-1" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseAttachmentsKinds(t *testing.T) {
	raw := `{"id":"EV-5","op":0,"t":"C2C_MESSAGE_CREATE","d":{
		"id":"m","content":"x","author":{"user_openid":"u"},
		"attachments":[
			{"content_type":"audio/silk","url":"u1"},
			{"content_type":"video/mp4","url":"u2"},
			{"content_type":"application/zip","url":"u3"}]}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	kinds := []PartKind{PartText, PartVoice, PartVideo, PartFile}
	if len(ev.Parts) != len(kinds) {
		t.Fatalf("parts = %+v", ev.Parts)
	}
	for i, want := range kinds {
		if ev.Parts[i].Kind != want {
			t.Fatalf("part %d kind = %q, want %q", i, ev.Parts[i].Kind, want)
		}
	}
}

func TestParseUnknownTypeIsKept(t *testing.T) {
	raw := `{"id":"EV-6","op":0,"t":"SOME_FUTURE_EVENT","d":{"foo":1}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if string(ev.Type) != "SOME_FUTURE_EVENT" || len(ev.Raw) == 0 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "not json", raw: `not-json`},
		{name: "missing t", raw: `{"id":"x","op":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.raw)); err == nil {
				t.Fatal("Parse() error = nil, want error")
			}
		})
	}
}

func TestParseBotAuthorAndFallbackID(t *testing.T) {
	raw := `{"id":"EV-7","op":0,"t":"GROUP_MESSAGE_CREATE","d":{
		"id":"m","group_openid":"g","author":{"id":"raw-id","username":"别的bot","bot":true}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !ev.Sender.Bot || ev.Sender.ID != "raw-id" {
		t.Fatalf("sender = %+v", ev.Sender)
	}
	if strings.TrimSpace(ev.Sender.Name) != "别的bot" {
		t.Fatalf("sender name = %q", ev.Sender.Name)
	}
}

func TestParseNewEventTypes(t *testing.T) {
	tests := []struct {
		typ   string
		scene Scene
	}{
		{"FRIEND_ADD", SceneC2C},
		{"FRIEND_DEL", SceneC2C},
		{"GROUP_MSG_RECEIVE", SceneGroup},
		{"GROUP_MSG_REJECT", SceneGroup},
		{"SUBSCRIBE_MESSAGE_STATUS", SceneC2C},
		{"GROUP_JOIN_REQUEST", SceneGroup},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			raw := `{"id":"EV-X","op":0,"t":"` + tt.typ + `","d":{}}`
			ev, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if string(ev.Type) != tt.typ || ev.Scene != tt.scene {
				t.Fatalf("type/scene = %q/%d, want %q/%d", ev.Type, ev.Scene, tt.typ, tt.scene)
			}
		})
	}
}

func TestParseTimestampFormats(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{name: "rfc3339", raw: `"2026-10-31T08:00:00+08:00"`, want: time.Date(2026, 10, 31, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))},
		{name: "unix seconds", raw: `1793400000`, want: time.Unix(1793400000, 0)},
		{name: "unix millis", raw: `1793400000000`, want: time.UnixMilli(1793400000000)},
		{name: "unix seconds string", raw: `"1793400000"`, want: time.Unix(1793400000, 0)},
		{name: "unix millis string", raw: `"1793400000000"`, want: time.UnixMilli(1793400000000)},
		{name: "garbage string", raw: `"not-a-time"`, want: time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"id":"EV-1","op":0,"t":"GROUP_ADD_ROBOT","d":{"timestamp":` + tt.raw + `}}`
			ev, err := Parse([]byte(body))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !ev.ReceivedAt.Equal(tt.want) {
				t.Fatalf("ReceivedAt = %v, want %v", ev.ReceivedAt, tt.want)
			}
		})
	}
}

func TestParseRefIdxStringForm(t *testing.T) {
	body := `{"id":"EV-1","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{
		"message_scene":{"ext":["REFIDX_ABC"]},
		"author":{"member_openid":"m1"},"group_openid":"g1"}}`
	ev, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.RefIdx != "REFIDX_ABC" {
		t.Fatalf("RefIdx = %q", ev.RefIdx)
	}
}

// 裸 base64 值自带 "=" padding，会被 strings.Cut 误切出未知 key——
// 不得因此丢弃，应落到裸字符串兜底。
func TestParseRefIdxBareBase64WithPadding(t *testing.T) {
	body := `{"id":"EV-1","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{
		"message_scene":{"ext":["REFIDX_ABC==","auth_token=tok"]},
		"author":{"member_openid":"m1"},"group_openid":"g1"}}`
	ev, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.RefIdx != "REFIDX_ABC==" {
		t.Fatalf("RefIdx = %q, want REFIDX_ABC==", ev.RefIdx)
	}
}

func TestParseControlEventWithNonObjectD(t *testing.T) {
	for _, body := range []string{
		`{"id":"EV-1","op":0,"t":"RESUMED","d":"sess-1"}`,
		`{"id":"EV-2","op":0,"t":"RESUMED","d":""}`,
	} {
		ev, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("Parse(%s) error = %v", body, err)
		}
		if string(ev.Type) != "RESUMED" {
			t.Fatalf("Type = %q", ev.Type)
		}
	}
}

// 新增字段（官方 2026-06 后文档）：mentions / ark_data / msg_elements 的解析。
func TestParseMentionsArkElements(t *testing.T) {
	raw := `{"id":"EV-10","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{
		"id":"ROBOT1.0_m10","group_openid":"g-1",
		"author":{"id":"a1","member_openid":"m-1","username":"张三","bot":false,"member_role":"member"},
		"content":"看看这个","message_type":3,
		"mentions":[{"id":"u2","member_openid":"m-2","username":"李四","bot":false}],
		"ark_data":{"prompt":"[每日打卡]快来完成今日学习打卡","ark_type":"miniapp","ark_name":"小程序",
			"fields":{"title":"快来完成今日学习打卡","source":"学习助手","jump_url":"https://example.com"}},
		"message_scene":{"source":"default","ext":["msg_idx=REFIDX_M10=="]},
		"timestamp":"2026-07-21T10:00:00+08:00"}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(ev.Mentions) != 1 || ev.Mentions[0].ID != "m-2" || ev.Mentions[0].Name != "李四" {
		t.Fatalf("mentions = %+v", ev.Mentions)
	}
	if ev.Ark == nil || ev.Ark.ArkType != "miniapp" || ev.Ark.ArkName != "小程序" {
		t.Fatalf("ark = %+v", ev.Ark)
	}
	if ev.Ark.Fields["title"] != "快来完成今日学习打卡" {
		t.Fatalf("ark fields = %+v", ev.Ark.Fields)
	}
}

func TestParseQuoteElements(t *testing.T) {
	raw := `{"id":"EV-11","op":0,"t":"C2C_MESSAGE_CREATE","d":{
		"id":"ROBOT1.0_m11","user_openid":"u-1",
		"author":{"id":"a1","user_openid":"u-1","username":"王五","bot":false},
		"content":"这个建议很有帮助","message_type":103,
		"msg_elements":[{"msg_idx":"REFIDX_Q1==","author":{"id":"a2","user_openid":"u-2","username":"赵六"},
			"message_type":0,"content":"每天坚持阅读半小时",
			"msg_elements":[{"msg_idx":"REFIDX_Q0==","message_type":0,"content":"嵌套引用"}]}],
		"message_scene":{"ext":["ref_msg_idx=REFIDX_Q1==","msg_idx=REFIDX_M11=="]}}}`
	ev, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if ev.MessageType != 103 || ev.RefMsgIdx != "REFIDX_Q1==" || ev.RefIdx != "REFIDX_M11==" {
		t.Fatalf("type/ref = %d/%q/%q", ev.MessageType, ev.RefMsgIdx, ev.RefIdx)
	}
	if len(ev.Elements) != 1 {
		t.Fatalf("elements = %+v", ev.Elements)
	}
	e := ev.Elements[0]
	if e.MsgIdx != "REFIDX_Q1==" || e.Author.ID != "u-2" || e.Author.Name != "赵六" {
		t.Fatalf("element[0] = %+v", e)
	}
	if len(e.Parts) != 1 || e.Parts[0].Kind != PartText || e.Parts[0].Text != "每天坚持阅读半小时" {
		t.Fatalf("element[0].parts = %+v", e.Parts)
	}
	// 递归嵌套：内层元素的正文落在 Parts[0].Text。
	if len(e.Elements) != 1 || len(e.Elements[0].Parts) != 1 || e.Elements[0].Parts[0].Text != "嵌套引用" {
		t.Fatalf("nested = %+v", e.Elements)
	}
}
