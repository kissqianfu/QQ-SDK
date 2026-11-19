package event

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse 用随机 payload 打解析器，锁死三个不变量：envelope 完整时必须成功、
// 事件头与 envelope 一致、MsgID 等于 d.id。
func FuzzParse(f *testing.F) {
	seeds := []string{
		// 官方格式：群 @ 消息 + msg_idx ext。
		`{"id":"EV-1","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{"content":"hi","group_openid":"g1","author":{"member_openid":"m1"},"message_scene":{"ext":["msg_idx=REFIDX_A=="]}}}`,
		// 单聊消息。
		`{"id":"EV-2","op":0,"t":"C2C_MESSAGE_CREATE","d":{"content":"yo","user_openid":"u1","author":{"user_openid":"u1"}}}`,
		// 互动事件：带历史前缀形态的 id + scene。
		`{"id":"EV-3","op":0,"t":"INTERACTION_CREATE","d":{"id":"INTERACTION_CREATE:i1","scene":"c2c","user_openid":"u1","type":11}}`,
		// 加群事件：d 里没有 id/author，只有 op_member_openid。
		`{"id":"EV-4","op":0,"t":"GROUP_ADD_ROBOT","d":{"group_openid":"g1","op_member_openid":"m1"}}`,
		// 好友添加：openid 在顶层。
		`{"id":"EV-5","op":0,"t":"FRIEND_ADD","d":{"openid":"u1","author":{"union_openid":"union1"}}}`,
		// 控制事件：d 是字符串（不能因此丢事件头）。
		`{"id":"EV-6","op":0,"t":"RESUMED","d":"resume-str"}`,
		// 未知事件类型。
		`{"id":"EV-7","op":0,"t":"UNKNOWN_TYPE","d":null}`,
		// 富附件 + 数字时间戳。
		`{"id":"EV-8","op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{"timestamp":1793400000000,"attachments":[{"content_type":"image/png","url":"https://x/y.png","width":10,"height":20},{"content_type":"voice","url":"https://x/v.silk","voice_wav_url":"https://x/v.wav","asr_refer_text":"你好"}]}}`,
		// mentions / ark_data / msg_elements（含递归嵌套）。
		`{"id":"EV-9","op":0,"t":"C2C_MESSAGE_CREATE","d":{"message_type":103,"user_openid":"u1","mentions":[{"id":"u2","username":"李四"}],"ark_data":{"ark_type":"miniapp","fields":{"title":"t","jump_url":"https://x"}},"msg_elements":[{"msg_idx":"R1==","content":"q","msg_elements":[{"msg_idx":"R0==","content":"n"}]}],"message_scene":{"ext":["ref_msg_idx=R1=="]}}}`,
		// 全量群消息不是 GROUP_AT，但平台用 mentions[].is_you 标记"@ 了机器人"。
		`{"id":"EV-10","op":0,"t":"GROUP_MESSAGE_CREATE","d":{"id":"m10","content":"hi","group_openid":"g1","mentions":[{"id":"b1","is_you":true}]}}`,
		`{"id":"EV-11","op":0,"t":"C2C_MESSAGE_CREATE","d":{"id":"m11","content":"hi","user_openid":"u1","mentions":[{"id":"b1","is_you":true}]}}`,
		// 病态嵌套元素（深度护栏回归）。
		`{"t":"GROUP_AT_MESSAGE_CREATE","d":{"msg_elements":[{"msg_elements":[{"msg_elements":["x"]}]}]}}`,
		// d 不是对象的各种形态。
		`{"t":"GROUP_AT_MESSAGE_CREATE","d":123}`,
		`{"t":"GROUP_AT_MESSAGE_CREATE","d":[1,2,3]}`,
		`{"t":"GROUP_AT_MESSAGE_CREATE","d":"str"}`,
		// 坏结构。
		`not json`,
		`{}`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		ev, err := Parse(raw)
		if err != nil {
			var env Envelope
			if jsonErr := json.Unmarshal(raw, &env); jsonErr == nil && env.T != "" {
				// 不变量：envelope 完整且 t 非空时，只有「消息类事件且 d 不是对象」才允许报错
				//（消息 d 坏必须让传输层感知，防幽灵事件）；控制事件一律容忍。
				if !IsMessageType(Type(env.T)) {
					t.Fatalf("Parse 拒绝了 envelope 完整且 t 非空的 payload: %v (raw=%q)", jsonErr, raw)
				}
				// 消息类事件允许在「d 解不进事件体结构」时报错（防幽灵事件）；
				// d 能正常解进 dispatchData 却被拒绝才算违规。
				var probe dispatchData
				if json.Unmarshal(env.D, &probe) == nil {
					t.Fatalf("消息类事件的 d 能解进事件体却仍被拒绝: %v (raw=%q)", jsonErr, raw)
				}
			}
			return
		}

		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("Parse 成功但 envelope 解不开: %v (raw=%q)", err, raw)
		}
		if ev.ID != env.ID {
			t.Fatalf("ID = %q, want %q", ev.ID, env.ID)
		}
		if string(ev.Type) != env.T {
			t.Fatalf("Type = %q, want %q", ev.Type, env.T)
		}
		if ev.Scene != SceneGroup && ev.Scene != SceneC2C {
			t.Fatalf("Scene = %d, want group(1)/c2c(2)", ev.Scene)
		}
		var d dispatchData
		_ = json.Unmarshal(env.D, &d)
		// AtMe 有两种来源：GROUP_AT_MESSAGE_CREATE 本身，或任意事件的 mentions[].is_you。
		wantAtMe := env.T == string(GroupAtMessageCreate)
		if !wantAtMe {
			for _, m := range d.Mentions {
				if m.IsYou {
					wantAtMe = true
					break
				}
			}
		}
		if ev.AtMe != wantAtMe {
			t.Fatalf("AtMe = %v for t=%q（want %v）", ev.AtMe, env.T, wantAtMe)
		}
		// MsgID 只对消息类事件赋值（互动/成员/申请等事件的 d.id 含义不同，不能混进来）。
		if IsMessageType(ev.Type) {
			if ev.MsgID != d.ID {
				t.Fatalf("MsgID = %q, want d.id %q", ev.MsgID, d.ID)
			}
		} else if ev.MsgID != "" {
			t.Fatalf("非消息事件 MsgID = %q，want 空（t=%q）", ev.MsgID, env.T)
		}
	})
}

// FuzzRefIdxs 打 refIdxs 的解析边界。核心不变量：官方 "key=value" 形式必须无损提取
// （历史 bug 就是整串带前缀返回），裸字符串必须落到 msg_idx 兜底。
func FuzzRefIdxs(f *testing.F) {
	f.Add("msg_idx=REFIDX_A==")
	f.Add("ref_msg_idx=REFIDX_B==")
	f.Add("REFIDX_bare")
	f.Add(`{"msg_idx":"A","ref_msg_idx":"B"}`)
	f.Add("")
	f.Add("null")
	f.Add("msg_idx=")
	f.Add("=msg_idx")
	f.Add("msg_idx==double==")
	f.Add("foo=msg_idx=bar")

	f.Fuzz(func(t *testing.T, v string) {
		// encoding/json 在编解码时会把非法 UTF-8 字节逐个替换为 U+FFFD，
		// 断言必须基于解析器实际收到的（归一后的）字符串，否则是 harness 伪 bug。
		v = jsonCoerceString(v)

		// 不变量 1：无首尾空白的 "msg_idx=<v>" / "ref_msg_idx=<v>" 必须精确提取出 v，
		// 且不产生另一个键的值（防"整串带前缀返回"回归）。
		// （refIdxs 对元素做 TrimSpace 归一，首尾空白输入不在精确提取的断言范围内。）
		if !hasEdgeSpace(v) {
			m, r := parseOneExt(t, "msg_idx="+v)
			if m != v || r != "" {
				t.Fatalf("官方 msg_idx 形式被破坏: got (%q, %q), want (%q, \"\")", m, r, v)
			}
			m, r = parseOneExt(t, "ref_msg_idx="+v)
			if r != v || m != "" {
				t.Fatalf("官方 ref_msg_idx 形式被破坏: got (%q, %q), want (\"\", %q)", m, r, v)
			}
		}

		// 不变量 2：裸字符串（无 = 非对象开头）兜底为 msg_idx。
		if !strings.Contains(v, "=") && !strings.HasPrefix(v, "{") && !hasEdgeSpace(v) {
			m, r := parseOneExt(t, v)
			if m != v || r != "" {
				t.Fatalf("裸字符串兜底被破坏: got (%q, %q), want (%q, \"\")", m, r, v)
			}
		}
	})
}

// parseOneExt 用单个 ext 元素跑一次 refIdxs。
func parseOneExt(t *testing.T, s string) (msgIdx, refMsgIdx string) {
	t.Helper()
	var d dispatchData
	d.MessageScene.Ext = []json.RawMessage{marshalString(t, s)}
	return d.refIdxs()
}

func hasEdgeSpace(s string) bool { return s != strings.TrimSpace(s) }

// jsonCoerceString 按 encoding/json 的规则归一字符串：每个非法 UTF-8 字节替换为一个 U+FFFD。
func jsonCoerceString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && w == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		b.WriteString(s[i : i+w])
		i += w
	}
	return b.String()
}

func marshalString(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal %q: %v", s, err)
	}
	return b
}
