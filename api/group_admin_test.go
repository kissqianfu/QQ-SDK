package api

// group_admin_test.go 覆盖 2026-08/09 文档新增的群管理接口。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kissqianfu/QQ-SDK/token"
)

func TestGroupMembersAndBlacklist(t *testing.T) {
	var gotPaths []string
	var gotQueries []string
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/members":
			gotPaths = append(gotPaths, r.URL.Path)
			gotQueries = append(gotQueries, r.URL.RawQuery)
			writeJSON(w, 200, `{"members":[{"member_openid":"m1","username":"甲","member_role":"admin","bot":false,"joined_at":"2026-01-01T00:00:00+08:00"}],"next_cursor":"c2"}`)
		case "/v2/groups/g1/members/m1":
			gotPaths = append(gotPaths, r.URL.Path)
			writeJSON(w, 200, `{"member_openid":"m1","username":"甲","member_role":"member"}`)
		case "/v2/groups/g1/member_blacklist":
			if r.Method == http.MethodGet {
				gotQueries = append(gotQueries, r.URL.RawQuery)
				writeJSON(w, 200, `{"users":[{"member_openid":"m9","username":"乙","banned_at":"2026-01-02T00:00:00+08:00"}],"next_cursor":""}`)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"fail_openids":["m8"]}`)
		case "/v2/groups/g1/batch_remove_members":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{"remove_members_result":"success","add_to_member_blacklist_fail_openids":["m7"]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	list, err := c.ListGroupMembers(context.Background(), "g1", "c1")
	if err != nil {
		t.Fatalf("ListGroupMembers() error = %v", err)
	}
	if len(list.Members) != 1 || list.Members[0].MemberRole != "admin" || list.NextCursor != "c2" {
		t.Fatalf("list = %+v", list)
	}
	if gotQueries[0] != "cursor=c1" {
		t.Fatalf("query = %q, want cursor=c1", gotQueries[0])
	}

	m, err := c.GroupMemberInfo(context.Background(), "g1", "m1")
	if err != nil || m.MemberOpenID != "m1" {
		t.Fatalf("GroupMemberInfo() = %+v, %v", m, err)
	}

	bl, err := c.ListMemberBlacklist(context.Background(), "g1", "", 0)
	if err != nil || len(bl.Users) != 1 {
		t.Fatalf("ListMemberBlacklist() = %+v, %v", bl, err)
	}
	if gotQueries[1] != "" {
		t.Fatalf("blacklist query = %q, want 空（cursor/limit 都不传）", gotQueries[1])
	}

	res, err := c.UpdateMemberBlacklist(context.Background(), "g1", MemberBlacklistRequest{
		Op: BlacklistOpAdd, MemberOpenIDs: []string{"m8"},
	})
	if err != nil || len(res.FailOpenIDs) != 1 {
		t.Fatalf("UpdateMemberBlacklist() = %+v, %v", res, err)
	}
	if gotBody["op"] != "add" {
		t.Fatalf("body = %+v", gotBody)
	}

	rm, err := c.BatchRemoveMembers(context.Background(), "g1", BatchRemoveMembersRequest{
		MemberOpenIDs: []string{"m7"}, AddToMemberBlacklist: true,
	})
	if err != nil || rm.RemoveMembersResult != "success" {
		t.Fatalf("BatchRemoveMembers() = %+v, %v", rm, err)
	}
	if gotBody["add_to_member_blacklist"] != true {
		t.Fatalf("batch remove body = %+v", gotBody)
	}
}

func TestSetGroupMemberMute(t *testing.T) {
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/restrict_chat_setting":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	expire := "2026-11-10T12:00:00+08:00"
	err := c.SetGroupMemberMute(context.Background(), "g1", []MemberMuteOp{
		{Op: MuteOpAdd, MemberOpenID: "m1", MuteExpireAt: expire},
		{Op: MuteOpDel, MemberOpenID: "m2"},
	})
	if err != nil {
		t.Fatalf("SetGroupMemberMute() error = %v", err)
	}
	members := gotBody["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %+v", gotBody["members"])
	}
	first := members[0].(map[string]any)
	if first["op"] != "add" || first["mute_expire_at"] != expire {
		t.Fatalf("members[0] = %+v", first)
	}

	// 本地校验：op 非法 / add 缺到期时间 / 超过 20 条。
	if err := c.SetGroupMemberMute(context.Background(), "g1", []MemberMuteOp{{Op: "bad", MemberOpenID: "m1"}}); err == nil {
		t.Fatal("非法 op 应该报错")
	}
	if err := c.SetGroupMemberMute(context.Background(), "g1", []MemberMuteOp{{Op: MuteOpAdd, MemberOpenID: "m1"}}); err == nil {
		t.Fatal("add 缺 mute_expire_at 应该报错")
	}
	if err := c.SetGroupMemberMute(context.Background(), "g1", []MemberMuteOp{{Op: MuteOpDel}}); err == nil {
		t.Fatal("缺 member_openid 应该报错")
	}
	ops := make([]MemberMuteOp, 21)
	for i := range ops {
		ops[i] = MemberMuteOp{Op: MuteOpDel, MemberOpenID: "m"}
	}
	if err := c.SetGroupMemberMute(context.Background(), "g1", ops); err == nil {
		t.Fatal("超过 20 条应该报错")
	}
	// 超过 30 天的到期时间本地拒绝。
	if err := c.SetGroupMemberMute(context.Background(), "g1", []MemberMuteOp{{
		Op: MuteOpAdd, MemberOpenID: "m1", MuteExpireAt: "2027-01-01T00:00:00+08:00",
	}}); err == nil {
		t.Fatal("超过 30 天应该报错")
	}
}

func TestJoinRequestListAndApproval(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
		case "/v2/groups/g1/join_request_list":
			writeJSON(w, 200, `{"list":[{"join_request_id":"j1","member_openid":"m1","username":"张三","apply_source":"self_apply","verify_info":{"method":"qa","review_qa_list":[{"question":"q","answer":"a"}]}}],"next_cursor":""}`)
		case "/v2/groups/g1/approval_join_request/m1":
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			writeJSON(w, 200, `{}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := newClient(t, srv, token.NewMemory())

	list, err := c.JoinRequestList(context.Background(), "g1", "", 0)
	if err != nil || len(list.List) != 1 {
		t.Fatalf("JoinRequestList() = %+v, %v", list, err)
	}
	jr := list.List[0]
	if jr.JoinRequestID != "j1" || jr.ApplySource != JoinApplySelf || jr.VerifyInfo == nil || len(jr.VerifyInfo.ReviewQAList) != 1 {
		t.Fatalf("join request = %+v", jr)
	}

	err = c.ApprovalJoinRequest(context.Background(), "g1", "m1", JoinApprovalRequest{
		Op: JoinOpDecline, JoinRequestID: "j1", RejectReason: "满员", AddToMemberBlacklist: true,
	})
	if err != nil {
		t.Fatalf("ApprovalJoinRequest() error = %v", err)
	}
	if gotPath != "/v2/groups/g1/approval_join_request/m1" || gotBody["op"] != "decline" || gotBody["join_request_id"] != "j1" {
		t.Fatalf("path=%q body=%+v", gotPath, gotBody)
	}

	// 本地校验。
	if err := c.ApprovalJoinRequest(context.Background(), "g1", "m1", JoinApprovalRequest{Op: "ok"}); err == nil {
		t.Fatal("非法 op 应该报错")
	}
	if err := c.ApprovalJoinRequest(context.Background(), "g1", "m1", JoinApprovalRequest{Op: JoinOpApprove}); err == nil {
		t.Fatal("缺 join_request_id 应该报错")
	}
}
