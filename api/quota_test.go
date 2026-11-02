package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/apierr"
	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/send"
	"github.com/kissqianfu/QQ-SDK/token"
)

// fakeClock 是可推进的时钟（配额要测"跨日重置""不补令牌"，不能用真实时间）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newQuotaClient 构造一个配额可配、时钟可控、并统计"发消息请求数"的客户端。
func newQuotaClient(t *testing.T, q *QuotaConfig) (*Client, *fakeClock, *atomic.Int32) {
	t.Helper()
	msgCalls := &atomic.Int32{}
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			writeJSON(w, 200, `{"access_token":"tok","expires_in":7200}`)
			return
		}
		msgCalls.Add(1)
		writeJSON(w, 200, `{"id":"m1","timestamp":"1700000000000"}`)
	})
	clk := &fakeClock{t: time.Date(2026, 11, 3, 0, 30, 0, 0, quotaLocation)}
	if q == nil {
		q = &QuotaConfig{}
	}
	if q.Now == nil {
		q.Now = clk.now
	}
	c, err := New(Config{
		AppID:     "app",
		AppSecret: "secret",
		BaseURL:   srv.URL,
		TokenURL:  srv.URL + "/app/getAppAccessToken",
		Tokens:    token.NewMemory(),
		Sleep:     func(time.Duration) {},
		Quota:     q,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c, clk, msgCalls
}

// sendActive 发一条主动消息（无锚点）。
func sendActive(t *testing.T, c *Client, scene event.Scene, id string) error {
	t.Helper()
	_, err := c.Send(context.Background(), Target{Scene: scene, ID: id}, send.Text("hi"))
	return err
}

// assertThrottled 断言错误是本地限流且层级符合预期。
func assertThrottled(t *testing.T, err error, layer string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望本地限流（layer=%s），实际成功", layer)
	}
	if !errors.Is(err, apierr.ErrThrottledLocal) {
		t.Fatalf("errors.Is(ErrThrottledLocal) = false, err = %v", err)
	}
	var te *apierr.ThrottleError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As(ThrottleError) = false, err = %v", err)
	}
	if te.Layer != layer {
		t.Fatalf("layer = %q, want %q", te.Layer, layer)
	}
}

// TestQuotaGroupBucket 群聊全局桶独立生效（60 QPM 口径在这里缩成 2）。
func TestQuotaGroupBucket(t *testing.T) {
	c, _, _ := newQuotaClient(t, &QuotaConfig{GroupQPM: 2})
	for i := 0; i < 2; i++ {
		if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
			t.Fatalf("第 %d 条应通过: %v", i+1, err)
		}
	}
	assertThrottled(t, sendActive(t, c, event.SceneGroup, "g1"), "group")
	if got := c.QuotaStats().Group; got != 1 {
		t.Fatalf("Stats.Group = %d, want 1", got)
	}
}

// TestQuotaGroupAndSingleIndependent 群聊与单聊两个全局桶互不影响。
func TestQuotaGroupAndSingleIndependent(t *testing.T) {
	c, _, _ := newQuotaClient(t, &QuotaConfig{SingleQPS: 1, GroupQPM: 5})
	if err := sendActive(t, c, event.SceneC2C, "u1"); err != nil {
		t.Fatalf("单聊第 1 条应通过: %v", err)
	}
	assertThrottled(t, sendActive(t, c, event.SceneC2C, "u2"), "single")

	// 单聊额度耗尽不影响群聊。
	if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
		t.Fatalf("群聊不该受单聊桶影响: %v", err)
	}
	if got := c.QuotaStats().Single; got != 1 {
		t.Fatalf("Stats.Single = %d, want 1", got)
	}
}

// TestQuotaRelationBucket 单关系桶按 openid 独立。
func TestQuotaRelationBucket(t *testing.T) {
	c, _, _ := newQuotaClient(t, &QuotaConfig{GroupQPM: 100, RelationQPM: 2})
	for i := 0; i < 2; i++ {
		if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
			t.Fatalf("第 %d 条应通过: %v", i+1, err)
		}
	}
	assertThrottled(t, sendActive(t, c, event.SceneGroup, "g1"), "relation")
	// 另一个目标有自己的桶。
	if err := sendActive(t, c, event.SceneGroup, "g2"); err != nil {
		t.Fatalf("另一个目标不该被连坐: %v", err)
	}
}

// TestQuotaRefundOuterToken 内层（单关系）拒绝时必须把外层（全局）令牌退回，
// 否则每次"外层放行、内层拒绝"都会白掉一个群聊额度。
func TestQuotaRefundOuterToken(t *testing.T) {
	// 全局只剩 2 个令牌：g1 用掉 1 个并占满它自己的关系桶，
	// 第 2 次 g1 会"过全局、被关系拒"，此时全局令牌必须退回，
	// 才能让随后的 g2 发出去。
	c, _, _ := newQuotaClient(t, &QuotaConfig{GroupQPM: 2, RelationQPM: 1})
	if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
		t.Fatalf("第 1 条应通过: %v", err)
	}
	assertThrottled(t, sendActive(t, c, event.SceneGroup, "g1"), "relation")
	if err := sendActive(t, c, event.SceneGroup, "g2"); err != nil {
		t.Fatalf("外层令牌未退回（g2 被全局桶误拒）: %v", err)
	}
}

// TestQuotaDailyLimitResetsNextDay 日上限只查不消耗，且跨日（UTC+8 00:00）重置。
func TestQuotaDailyLimitResetsNextDay(t *testing.T) {
	c, clk, _ := newQuotaClient(t, &QuotaConfig{GroupQPM: 1000, DailyPerTarget: 2})
	for i := 0; i < 2; i++ {
		if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
			t.Fatalf("第 %d 条应通过: %v", i+1, err)
		}
	}
	assertThrottled(t, sendActive(t, c, event.SceneGroup, "g1"), "daily")
	if got := c.QuotaStats().Daily; got != 1 {
		t.Fatalf("Stats.Daily = %d, want 1", got)
	}

	// 跨到 UTC+8 次日：日计数清零（时钟从 00:30 推进 24h）。
	clk.advance(24 * time.Hour)
	if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
		t.Fatalf("跨日后应恢复: %v", err)
	}
}

// TestQuotaPassiveBypass 被动回复（有 msg_id）不计数、不消耗任何层。
func TestQuotaPassiveBypass(t *testing.T) {
	c, _, _ := newQuotaClient(t, &QuotaConfig{GroupQPM: 1, SingleQPS: 1, RelationQPM: 1, DailyPerTarget: 1})
	for i := 0; i < 10; i++ {
		_, err := c.Send(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"},
			send.Text("回").ReplyTo("msg-1", i+1))
		if err != nil {
			t.Fatalf("被动回复第 %d 条被拦: %v", i+1, err)
		}
	}
	if got := c.QuotaStats(); got != (QuotaStats{}) {
		t.Fatalf("被动回复不该计数: %+v", got)
	}
}

// TestQuotaKeysBounded 单关系桶的键表有上限（防被海量 openid 撑爆内存）。
func TestQuotaKeysBounded(t *testing.T) {
	q := newQuota(&QuotaConfig{GroupQPM: 1 << 30, RelationQPM: 20, DailyPerTarget: 1 << 30})
	if q == nil {
		t.Fatal("newQuota 返回 nil")
	}
	// 直接打 acquire（不经 HTTP）：这里验的是键表容量，不是网络路径。
	for i := 0; i < keyCap+500; i++ {
		id := "g" + itoa(i)
		if err := q.acquire(event.SceneGroup, id); err != nil {
			t.Fatalf("第 %d 条应通过: %v", i, err)
		}
	}
	if len(q.relations) > keyCap {
		t.Fatalf("关系表 %d 超过上限 %d", len(q.relations), keyCap)
	}
	if len(q.daily) > keyCap {
		t.Fatalf("日计数表 %d 超过上限 %d", len(q.daily), keyCap)
	}
}

// TestQuotaRejectNeverCallsPlatform 本地限流时**一个请求都不发**（否则"省额度"无从谈起）。
func TestQuotaRejectNeverCallsPlatform(t *testing.T) {
	c, _, msgCalls := newQuotaClient(t, &QuotaConfig{GroupQPM: 1})
	if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
		t.Fatalf("第 1 条应通过: %v", err)
	}
	before := msgCalls.Load()
	assertThrottled(t, sendActive(t, c, event.SceneGroup, "g1"), "group")
	if after := msgCalls.Load(); after != before {
		t.Fatalf("本地限流后仍发了 %d 个请求（应 0）", after-before)
	}
}

// TestQuotaDisabled Config.Quota.Disable 时整体关闭。
func TestQuotaDisabled(t *testing.T) {
	c, _, _ := newQuotaClient(t, &QuotaConfig{Disable: true, GroupQPM: 1})
	for i := 0; i < 5; i++ {
		if err := sendActive(t, c, event.SceneGroup, "g1"); err != nil {
			t.Fatalf("关闭配额后第 %d 条被拦: %v", i+1, err)
		}
	}
	if got := c.QuotaStats(); got != (QuotaStats{}) {
		t.Fatalf("关闭时不该有统计: %+v", got)
	}
}

// TestLocalThrottleNotConfusedWith429 本地限流错误与平台 429 必须可区分：
// 前者不重试，后者才值得等 Retry-After 重试。
func TestLocalThrottleNotConfusedWith429(t *testing.T) {
	local := &apierr.ThrottleError{Layer: "relation", RetryAfter: 3 * time.Second}
	if !errors.Is(local, apierr.ErrThrottledLocal) {
		t.Fatal("ThrottleError 应满足 errors.Is(ErrThrottledLocal)")
	}
	platform := &apierr.APIError{Code: 40034100, HTTP: 429}
	if errors.Is(platform, apierr.ErrThrottledLocal) {
		t.Fatal("平台 429 不该被当成本地限流")
	}
	if !platform.IsRateLimited() {
		t.Fatal("平台 429 应满足 IsRateLimited")
	}
	if errors.Is(local, platform) {
		t.Fatal("本地限流不该等于平台错误")
	}
}

// itoa 小工具（避免为一个用例引入 strconv 与 fmt 的歧义）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
