package api

// join_approval_test.go 覆盖入群自动审批策略的六个接口、白名单分批与本地校验。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kissqianfu/QQ-SDK/core/token"
)

// fakeStrategyServer 记录请求方法与 body，按路径返回固定响应。
type strategyReq struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

func newStrategyServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *[]strategyReq) {
	t.Helper()
	var got []strategyReq
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
			return
		}
		rec := strategyReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&rec.Body)
		}
		got = append(got, rec)
		handler(w, r)
	})
	return newClient(t, srv, token.NewMemory()), &got
}

func TestJoinApprovalStrategyCRUD(t *testing.T) {
	c, got := newStrategyServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/groups/join_approval_strategy" && r.Method == http.MethodGet:
			writeJSON(w, 200, `{"strategies":[
				{"strategy_id":"st_1","group_openids":["g1"],"group_ids":[],
				 "whitelist_user_count":2,"is_enable":"on",
				 "expire_at":"2027-08-05T15:30:16+08:00","created_at":"2026-08-05T15:30:16+08:00",
				 "updated_at":"2026-08-05T15:45:28+08:00","remark":"内部群"},
				{"strategy_id":"st_2","group_openids":[],"group_ids":["123456789"],
				 "whitelist_user_count":0,"is_enable":"off"}],
				"next_cursor":"c2"}`)
		case r.URL.Path == "/v2/groups/join_approval_strategy" && r.Method == http.MethodPost:
			writeJSON(w, 200, `{"strategy_id":"st_new","is_enable":"on","expire_at":"2027-08-05T15:30:16+08:00"}`)
		case r.URL.Path == "/v2/groups/join_approval_strategy/st_1" && r.Method == http.MethodPatch:
			writeJSON(w, 200, `{"is_enable":"off","expire_at":"2027-08-05T15:30:16+08:00"}`)
		case r.URL.Path == "/v2/groups/join_approval_strategy/st_1" && r.Method == http.MethodDelete:
			writeJSON(w, 200, `{}`)
		case r.URL.Path == "/v2/groups/join_approval_strategy/st_1/execute":
			writeJSON(w, 200, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	list, err := c.ListJoinApprovalStrategies(context.Background(), "c1", 30)
	if err != nil {
		t.Fatalf("ListJoinApprovalStrategies() error = %v", err)
	}
	if len(list.Strategies) != 2 || list.NextCursor != "c2" {
		t.Fatalf("list = %+v", list)
	}
	if !list.Strategies[0].Enabled() || list.Strategies[1].Enabled() {
		t.Fatalf("Enabled() 判定错误: %+v", list.Strategies)
	}
	if list.Strategies[0].WhitelistUserCount != 2 || list.Strategies[0].Remark != "内部群" {
		t.Fatalf("策略字段解析错误: %+v", list.Strategies[0])
	}
	// group_ids 文档示例是字符串，必须能解析成 uint64。
	if len(list.Strategies[1].GroupIDs) != 1 || list.Strategies[1].GroupIDs[0] != 123456789 {
		t.Fatalf("group_ids 解析错误: %+v", list.Strategies[1].GroupIDs)
	}
	if (*got)[0].Query != "cursor=c1&limit=30" {
		t.Fatalf("query = %q", (*got)[0].Query)
	}

	st, err := c.CreateJoinApprovalStrategy(context.Background(), CreateJoinApprovalStrategyRequest{
		GroupIDs: GroupIDs{123456789},
		IsEnable: StrategyEnableOn,
		Remark:   "白名单自动放行",
	})
	if err != nil || st.StrategyID != "st_new" {
		t.Fatalf("CreateJoinApprovalStrategy() = %+v, %v", st, err)
	}
	body := (*got)[1].Body
	if _, ok := body["group_openids"]; ok {
		t.Fatalf("未用的群标识不该出现在 body: %+v", body)
	}
	ids, ok := body["group_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0].(float64) != 123456789 {
		t.Fatalf("group_ids 应按数字发出: %+v", body["group_ids"])
	}

	up, err := c.UpdateJoinApprovalStrategy(context.Background(), "st_1", UpdateJoinApprovalStrategyRequest{
		GroupAction: &StrategyGroupAction{Op: ApprovalOpAdd, GroupOpenIDs: []string{"g2"}},
	})
	if err != nil || up.IsEnable != StrategyEnableOff {
		t.Fatalf("UpdateJoinApprovalStrategy() = %+v, %v", up, err)
	}
	act, ok := (*got)[2].Body["group_action"].(map[string]any)
	if !ok || act["op"] != "add" {
		t.Fatalf("group_action body = %+v", (*got)[2].Body)
	}

	if err := c.DeleteJoinApprovalStrategy(context.Background(), "st_1"); err != nil {
		t.Fatalf("DeleteJoinApprovalStrategy() error = %v", err)
	}
	if (*got)[3].Method != http.MethodDelete {
		t.Fatalf("delete method = %q", (*got)[3].Method)
	}

	if err := c.ExecuteJoinApprovalStrategy(context.Background(), "st_1"); err != nil {
		t.Fatalf("ExecuteJoinApprovalStrategy() error = %v", err)
	}
	if (*got)[4].Method != http.MethodPost || (*got)[4].Path != "/v2/groups/join_approval_strategy/st_1/execute" {
		t.Fatalf("execute = %+v", (*got)[4])
	}
}

func TestListJoinApprovalStrategiesAll(t *testing.T) {
	c, got := newStrategyServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			writeJSON(w, 200, `{"strategies":[{"strategy_id":"st_1"}],"next_cursor":"c2"}`)
			return
		}
		writeJSON(w, 200, `{"strategies":[{"strategy_id":"st_2"}],"next_cursor":""}`)
	})

	all, err := c.ListJoinApprovalStrategiesAll(context.Background())
	if err != nil {
		t.Fatalf("ListJoinApprovalStrategiesAll() error = %v", err)
	}
	if len(all) != 2 || all[1].StrategyID != "st_2" {
		t.Fatalf("all = %+v", all)
	}
	if n := len(*got); n != 2 {
		t.Fatalf("请求次数 = %d, want 2", n)
	}
}

func TestJoinApprovalWhitelist(t *testing.T) {
	c, got := newStrategyServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"strategy_id":"st_1","whitelist_user_count":2,"updated_at":"2026-08-05T15:45:28+08:00"}`)
	})

	res, err := c.UpdateJoinApprovalWhitelist(context.Background(), "st_1", JoinApprovalWhitelistRequest{
		Op:             ApprovalOpAdd,
		WhitelistUsers: []string{"1234567", "1234568"},
	})
	if err != nil || res.WhitelistUserCount != 2 {
		t.Fatalf("UpdateJoinApprovalWhitelist() = %+v, %v", res, err)
	}
	if (*got)[0].Path != "/v2/groups/join_approval_strategy/st_1/whitelist_users" {
		t.Fatalf("path = %q", (*got)[0].Path)
	}
	users, _ := (*got)[0].Body["whitelist_users"].([]any)
	if len(users) != 2 || users[0] != "1234567" {
		t.Fatalf("whitelist_users 应按字符串发出: %+v", (*got)[0].Body)
	}
}

func TestJoinApprovalWhitelistAllChunks(t *testing.T) {
	c, got := newStrategyServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"strategy_id":"st_1","whitelist_user_count":10000}`)
	})

	users := make([]string, maxWhitelistPerCall+1)
	for i := range users {
		users[i] = "12345"
	}
	if _, err := c.UpdateJoinApprovalWhitelistAll(context.Background(), "st_1",
		JoinApprovalWhitelistRequest{Op: ApprovalOpAdd, WhitelistUsers: users}); err != nil {
		t.Fatalf("UpdateJoinApprovalWhitelistAll() error = %v", err)
	}
	if n := len(*got); n != 2 {
		t.Fatalf("分批数 = %d, want 2", n)
	}
	first, _ := (*got)[0].Body["whitelist_users"].([]any)
	second, _ := (*got)[1].Body["whitelist_users"].([]any)
	if len(first) != maxWhitelistPerCall || len(second) != 1 {
		t.Fatalf("分批大小 = %d/%d", len(first), len(second))
	}
}

func TestJoinApprovalWhitelistAllPartialFailure(t *testing.T) {
	calls := 0
	c, _ := newStrategyServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(w, 200, `{"strategy_id":"st_1","whitelist_user_count":10000}`)
			return
		}
		writeJSON(w, 400, `{"err_code":11253,"message":"无权限"}`)
	})

	users := make([]string, maxWhitelistPerCall+1)
	for i := range users {
		users[i] = "12345"
	}
	_, err := c.UpdateJoinApprovalWhitelistAll(context.Background(), "st_1",
		JoinApprovalWhitelistRequest{Op: ApprovalOpAdd, WhitelistUsers: users})
	if err == nil {
		t.Fatal("第二批失败应返回错误")
	}
	// 错误里要给出进度，否则调用方不知道已经生效了多少。
	if !strings.Contains(err.Error(), "前 10000 个已生效") {
		t.Fatalf("err = %v, 缺少进度信息", err)
	}
}

func TestJoinApprovalLocalValidation(t *testing.T) {
	c := noServerClient(t)
	ctx := context.Background()
	bothRefs := []string{"g1"}

	cases := []struct {
		name string
		call func() error
	}{
		{"创建：两种群标识都未给", func() error {
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{})
			return err
		}},
		{"创建：两种群标识同时给", func() error {
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{
				GroupOpenIDs: bothRefs, GroupIDs: GroupIDs{123456789}})
			return err
		}},
		{"创建：关联群超 100", func() error {
			ids := make([]string, maxStrategyGroups+1)
			for i := range ids {
				ids[i] = "g"
			}
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{GroupOpenIDs: ids})
			return err
		}},
		{"创建：openid 有空值", func() error {
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{
				GroupOpenIDs: []string{" "}})
			return err
		}},
		{"创建：is_enable 非法", func() error {
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{
				GroupOpenIDs: bothRefs, IsEnable: "yes"})
			return err
		}},
		{"创建：expire_at 非 RFC3339", func() error {
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{
				GroupOpenIDs: bothRefs, ExpireAt: "2027-08-05"})
			return err
		}},
		{"创建：remark 超 255 字", func() error {
			_, err := c.CreateJoinApprovalStrategy(ctx, CreateJoinApprovalStrategyRequest{
				GroupOpenIDs: bothRefs, Remark: strings.Repeat("备", maxStrategyRemarkRunes+1)})
			return err
		}},
		{"修改：一项都没给", func() error {
			_, err := c.UpdateJoinApprovalStrategy(ctx, "st_1", UpdateJoinApprovalStrategyRequest{})
			return err
		}},
		{"修改：strategy_id 为空", func() error {
			_, err := c.UpdateJoinApprovalStrategy(ctx, "", UpdateJoinApprovalStrategyRequest{
				IsEnable: StrategyEnableOff})
			return err
		}},
		{"修改：group_action.op 非法", func() error {
			_, err := c.UpdateJoinApprovalStrategy(ctx, "st_1", UpdateJoinApprovalStrategyRequest{
				GroupAction: &StrategyGroupAction{Op: "set", GroupOpenIDs: bothRefs}})
			return err
		}},
		{"修改：group_action 未给群标识", func() error {
			_, err := c.UpdateJoinApprovalStrategy(ctx, "st_1", UpdateJoinApprovalStrategyRequest{
				GroupAction: &StrategyGroupAction{Op: ApprovalOpAdd}})
			return err
		}},
		{"删除：strategy_id 为空", func() error {
			return c.DeleteJoinApprovalStrategy(ctx, " ")
		}},
		{"执行：strategy_id 为空", func() error {
			return c.ExecuteJoinApprovalStrategy(ctx, "")
		}},
		{"白名单：op 非法", func() error {
			_, err := c.UpdateJoinApprovalWhitelist(ctx, "st_1", JoinApprovalWhitelistRequest{
				Op: "set", WhitelistUsers: []string{"12345"}})
			return err
		}},
		{"白名单：空列表", func() error {
			_, err := c.UpdateJoinApprovalWhitelist(ctx, "st_1", JoinApprovalWhitelistRequest{
				Op: ApprovalOpAdd})
			return err
		}},
		{"白名单：单次超 10000", func() error {
			users := make([]string, maxWhitelistPerCall+1)
			for i := range users {
				users[i] = "12345"
			}
			_, err := c.UpdateJoinApprovalWhitelist(ctx, "st_1", JoinApprovalWhitelistRequest{
				Op: ApprovalOpAdd, WhitelistUsers: users})
			return err
		}},
		{"白名单：号码非数字", func() error {
			_, err := c.UpdateJoinApprovalWhitelist(ctx, "st_1", JoinApprovalWhitelistRequest{
				Op: ApprovalOpAdd, WhitelistUsers: []string{"abc"}})
			return err
		}},
		{"白名单：号码为空串", func() error {
			_, err := c.UpdateJoinApprovalWhitelist(ctx, "st_1", JoinApprovalWhitelistRequest{
				Op: ApprovalOpAdd, WhitelistUsers: []string{""}})
			return err
		}},
		{"白名单分批：空列表", func() error {
			_, err := c.UpdateJoinApprovalWhitelistAll(ctx, "st_1", JoinApprovalWhitelistRequest{
				Op: ApprovalOpAdd})
			return err
		}},
		{"列表：limit 为负", func() error {
			_, err := c.ListJoinApprovalStrategies(ctx, "", -1)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatalf("%s: error = nil, want 本地校验失败", tc.name)
			}
		})
	}
}

func TestGroupIDsUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want GroupIDs
	}{
		{"数字", `[123456789,987654321]`, GroupIDs{123456789, 987654321}},
		{"字符串", `["123456789","987654321"]`, GroupIDs{123456789, 987654321}},
		{"混合", `[123456789,"987654321"]`, GroupIDs{123456789, 987654321}},
		{"空数组", `[]`, GroupIDs{}},
		{"null 元素跳过", `[123456789,null]`, GroupIDs{123456789}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got GroupIDs
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.raw, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got = %+v, want %+v", got, tc.want)
				}
			}
		})
	}

	var bad GroupIDs
	if err := json.Unmarshal([]byte(`["10****499"]`), &bad); err == nil {
		t.Fatal("掩码字符串应解析失败")
	}
}
