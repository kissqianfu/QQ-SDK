package api

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestGroupInfo(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/groups/G1/info" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, `{"group_openid":"G1","group_name":"读书分享会","group_finger_memo":"每周共读一本好书","group_class_text":"文化","group_tags":["阅读","文学"],"group_member_num":256}`)
	})
	c := newClient(t, srv, seededToken(t))

	info, err := c.GroupInfo(context.Background(), "G1")
	if err != nil {
		t.Fatalf("GroupInfo() error = %v", err)
	}
	if info.GroupName != "读书分享会" || info.GroupMemberNum != 256 ||
		len(info.GroupTags) != 2 || info.GroupClassText != "文化" {
		t.Fatalf("info = %+v", info)
	}
}

func TestGroupBotState(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/groups/G1/bot_state" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, `{"member_openid":"B1","joined_at":"2025-06-15T14:30:00+08:00","allow_proactive_msg":false,"recv_msg_setting":"only_mention","member_role":"member"}`)
	})
	c := newClient(t, srv, seededToken(t))

	st, err := c.GroupBotState(context.Background(), "G1")
	if err != nil {
		t.Fatalf("GroupBotState() error = %v", err)
	}
	if st.MemberOpenID != "B1" || st.RecvMsgSetting != "only_mention" || st.MemberRole != "member" {
		t.Fatalf("state = %+v", st)
	}
	if _, err := time.Parse("2006-01-02T15:04:05Z07:00", st.JoinedAt); err != nil {
		t.Fatalf("joined_at 非 RFC3339: %v", err)
	}
}

func TestGroupRestrictSetting(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/groups/G1/restrict_chat_setting" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, `{
			"global_rule": {
				"mode": "schedule",
				"schedule_rules": [{"task_id":"t1","start_at":"2026-07-22T10:44:00+08:00","end_at":"2026-07-22T11:44:00+08:00","enabled":false}],
				"recurring_rules": [{"task_id":"t2","weekdays":[1,2,3],"start_time":"13:05","end_time":"14:05","enabled":true}]
			},
			"members": [{"member_openid":"M1","mute_expire_at":"2026-08-05T11:23:04+08:00","username":"小明","union_openid":"U1"}]
		}`)
	})
	c := newClient(t, srv, seededToken(t))

	rs, err := c.GroupRestrictSetting(context.Background(), "G1")
	if err != nil {
		t.Fatalf("GroupRestrictSetting() error = %v", err)
	}
	if rs.GlobalRule == nil || rs.GlobalRule.Mode != "schedule" {
		t.Fatalf("global_rule = %+v", rs.GlobalRule)
	}
	if len(rs.GlobalRule.ScheduleRules) != 1 || len(rs.GlobalRule.RecurringRules) != 1 {
		t.Fatalf("rules = %+v", rs.GlobalRule)
	}
	rec := rs.GlobalRule.RecurringRules[0]
	if rec.TaskID != "t2" || len(rec.Weekdays) != 3 || rec.StartTime != "13:05" || !rec.Enabled {
		t.Fatalf("recurring = %+v", rec)
	}
	if len(rs.Members) != 1 || rs.Members[0].MemberOpenID != "M1" || rs.Members[0].Username != "小明" {
		t.Fatalf("members = %+v", rs.Members)
	}
}

func TestGroupEmptyOpenID(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, _ *http.Request) {}), seededToken(t))
	if _, err := c.GroupInfo(context.Background(), ""); err == nil {
		t.Fatal("GroupInfo(\"\") error = nil, want 校验错误")
	}
	if _, err := c.GroupBotState(context.Background(), ""); err == nil {
		t.Fatal("GroupBotState(\"\") error = nil, want 校验错误")
	}
	if _, err := c.GroupRestrictSetting(context.Background(), ""); err == nil {
		t.Fatal("GroupRestrictSetting(\"\") error = nil, want 校验错误")
	}
}
