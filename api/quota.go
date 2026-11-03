package api

import (
	"sync"
	"time"

	"github.com/kissqianfu/QQ-SDK/apierr"
	"github.com/kissqianfu/QQ-SDK/event"
)

// 本文件是**出站主动消息三层配额**的唯一实现。
//
// 为什么放在 SDK 的发送层（Client.Send）而不是各宿主项目里：
//   - 所有发送形态（文本 / Markdown / 卡片 / 富媒体）都收敛到 Send 这一个出口，
//     放在这里天然覆盖全部形态，宿主不必逐个方法记得加；
//   - 配额是**协议侧**的自保（平台会 429，本地先拦），放 SDK 才能跟着版本一起演进；
//   - 与 SDK 的 429 重试是同一处代码，避免"限流 × 重试"互相放大。
//
// 三层依次校验（与 README 的表格一一对应）：
//
//	① 全局桶：群聊 60 QPM、单聊 10 QPS —— 两个桶**相互独立**（群消息与单聊消息的
//	   平台额度本来就分开计），都要放行才发送；
//	② 单关系桶：按 group_openid / user_openid 各 20 QPM；
//	③ 日上限：每目标 1000 条 / 天（日界 UTC+8 00:00），**只查不消耗**。
//
// ⚠️ 语义边界（写进 README 与注释，避免被当成权威额度）：
//   - 纯内存，重启即重置；多实例之间也不共享；
//   - 因此它是 **best-effort**：用来在告警风暴时保护自己与平台，
//     真正权威的口径永远是平台返回的 429；
//   - 被本地拒绝时**绝不调用平台**（否则"省额度"就无从谈起）。

// 默认口径。
const (
	defaultGroupQPM    = 60
	defaultSingleQPS   = 10
	defaultRelationQPM = 20
	defaultDailyLimit  = 1000
	// keyCap 是单关系桶与日计数表的键上限：防止被大量不同 openid 撑爆内存。
	// 超出后按先进先出淘汰最老的键——被淘汰的关系等于"额度重新开始"，可接受。
	keyCap = 4096
)

// quotaLocation 是日上限的日界时区（官方口径 UTC+8）。
var quotaLocation = time.FixedZone("UTC+8", 8*3600)

// QuotaConfig 是三层配额参数；零值字段一律取默认值。
//
// Disable 为 true 时**整体关闭**（宿主/测试需要绕过配额时用）。
type QuotaConfig struct {
	Disable        bool
	GroupQPM       int            // 群聊全局：每分钟条数（默认 60）
	SingleQPS      int            // 单聊全局：每秒条数（默认 10）
	RelationQPM    int            // 单关系：每分钟条数（默认 20）
	DailyPerTarget int            // 每目标每日上限（默认 1000）
	Location       *time.Location // 日界时区（默认 UTC+8）
	// Now 可注入，测试用来推进时间/跨日；nil 用 time.Now。
	Now func() time.Time
}

// normalize 填默认值并把负数/零归一为"关闭该层"。
func (c QuotaConfig) normalize() QuotaConfig {
	if c.GroupQPM == 0 {
		c.GroupQPM = defaultGroupQPM
	}
	if c.SingleQPS == 0 {
		c.SingleQPS = defaultSingleQPS
	}
	if c.RelationQPM == 0 {
		c.RelationQPM = defaultRelationQPM
	}
	if c.DailyPerTarget == 0 {
		c.DailyPerTarget = defaultDailyLimit
	}
	if c.Location == nil {
		c.Location = quotaLocation
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// QuotaStats 是三层各自的拒绝计数，供宿主导出指标（如
// bot_messages_throttled_total{layer}）。
type QuotaStats struct {
	Group    uint64 // 群聊全局桶拒绝
	Single   uint64 // 单聊全局桶拒绝
	Relation uint64 // 单关系桶拒绝
	Daily    uint64 // 日上限拒绝
}

// bucket 是标准令牌桶：tokens 个令牌，每秒补 rate 个，上限 burst。
type bucket struct {
	tokens float64
	last   time.Time
	rate   float64 // 每秒补充
	burst  float64 // 容量上限
}

func newBucket(perSecond, burst float64, now time.Time) *bucket {
	return &bucket{tokens: burst, last: now, rate: perSecond, burst: burst}
}

// refill 按经过时间补令牌。
func (b *bucket) refill(now time.Time) {
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
}

// take 尝试取一个令牌；失败时给出"大约还要等多久"。
func (b *bucket) take(now time.Time) (bool, time.Duration) {
	b.refill(now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	if b.rate <= 0 {
		return false, 0
	}
	need := (1 - b.tokens) / b.rate
	return false, time.Duration(need * float64(time.Second))
}

// refund 退回一个令牌（内层拒绝时把外层已扣的还回去）。
//
// ⚠️ 不退的话，每次"外层放行、内层拒绝"都会白掉一个全局令牌：
// 告警风暴里被单关系限流的那批消息会顺带把自己的群聊额度耗尽。
func (b *bucket) refund(now time.Time) {
	b.refill(now)
	b.tokens++
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

// quota 是三层配额的运行时状态。
type quota struct {
	cfg QuotaConfig

	mu        sync.Mutex
	group     *bucket
	single    *bucket
	relations map[string]*bucket
	relOrder  []string // 单关系桶的 FIFO 淘汰顺序

	daily     map[string]int
	dailyDay  string
	dailyKeys []string

	stats QuotaStats
}

// newQuota 构造配额；cfg 为 nil 或 Disable 时返回 nil（调用方 nil 安全）。
func newQuota(cfg *QuotaConfig) *quota {
	if cfg == nil || cfg.Disable {
		return nil
	}
	c := cfg.normalize()
	now := c.Now()
	q := &quota{
		cfg:       c,
		relations: make(map[string]*bucket),
		daily:     make(map[string]int),
		dailyDay:  dayOf(now, c.Location),
	}
	if c.GroupQPM > 0 {
		q.group = newBucket(float64(c.GroupQPM)/60, float64(c.GroupQPM), now)
	}
	if c.SingleQPS > 0 {
		q.single = newBucket(float64(c.SingleQPS), float64(c.SingleQPS), now)
	}
	return q
}

// dayOf 取指定时区下的日期串（日界用）。
func dayOf(now time.Time, loc *time.Location) string {
	return now.In(loc).Format("2006-01-02")
}

// acquire 为一条**主动**消息申请配额；被动回复不应调用本方法。
//
// 顺序是刻意的：先"只查不消耗"地看日上限，再扣两层桶令牌，
// 全部通过后才把日计数 +1——这样任何一层拒绝都不会留下计数。
func (q *quota) acquire(scene event.Scene, targetID string) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.cfg.Now()
	q.rollDay(now)

	// ① 日上限：只查不消耗，超了立刻拒（不白掉桶令牌）。
	if q.cfg.DailyPerTarget > 0 && q.daily[targetID] >= q.cfg.DailyPerTarget {
		q.stats.Daily++
		return &apierr.ThrottleError{Layer: "daily"}
	}

	// ② 全局桶：群聊与单聊各一条，互不影响。
	gb, layer := q.group, "group"
	if scene != event.SceneGroup {
		gb, layer = q.single, "single"
	}
	if gb != nil {
		ok, wait := gb.take(now)
		if !ok {
			if layer == "group" {
				q.stats.Group++
			} else {
				q.stats.Single++
			}
			return &apierr.ThrottleError{Layer: layer, RetryAfter: wait}
		}
	}

	// ③ 单关系桶：失败要把 ② 的令牌退回。
	if rb := q.relationBucket(targetID, now); rb != nil {
		ok, wait := rb.take(now)
		if !ok {
			if gb != nil {
				gb.refund(now)
			}
			q.stats.Relation++
			return &apierr.ThrottleError{Layer: "relation", RetryAfter: wait}
		}
	}

	// 日计数不在这里 +1：只有真正发送成功才计（见 noteSent），
	// 否则请求失败也会白占当日额度。
	return nil
}

// noteSent 记一条"已经成功发出"的主动消息。
//
// ⚠️ 日上限只统计成功发出的：`acquire` 阶段只查不计数，发送失败（平台 4xx/5xx/ctx 取消）
// 不会白占当日额度——否则一轮故障就能把目标当天的 1000 条额度打光。
func (q *quota) noteSent(targetID string) {
	if q == nil || q.cfg.DailyPerTarget <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollDay(q.cfg.Now())
	q.noteDaily(targetID)
}

// relationBucket 取（必要时创建）某个目标的桶；键表满时淘汰最老的键。
func (q *quota) relationBucket(id string, now time.Time) *bucket {
	if q.cfg.RelationQPM <= 0 {
		return nil
	}
	if b, ok := q.relations[id]; ok {
		return b
	}
	if len(q.relations) >= keyCap {
		q.relOrder = evictOne(q.relations, q.relOrder)
	}
	b := newBucket(float64(q.cfg.RelationQPM)/60, float64(q.cfg.RelationQPM), now)
	q.relations[id] = b
	q.relOrder = append(q.relOrder, id)
	return b
}

// noteDaily 记一次当日成功发送（调用前已确认过日上限）。
func (q *quota) noteDaily(id string) {
	if _, ok := q.daily[id]; !ok {
		if len(q.daily) >= keyCap {
			q.dailyKeys = evictOne(q.daily, q.dailyKeys)
		}
		q.dailyKeys = append(q.dailyKeys, id)
	}
	q.daily[id]++
}

// rollDay 跨日时清空日计数。
func (q *quota) rollDay(now time.Time) {
	day := dayOf(now, q.cfg.Location)
	if day == q.dailyDay {
		return
	}
	q.dailyDay = day
	q.daily = make(map[string]int)
	q.dailyKeys = nil
}

// evictOne 按 FIFO 从 m 删掉一个键，并返回裁剪后的顺序表。
//
// 顺序表里可能有已不存在的键（同一目标被淘汰后又重新加入），逐个跳过即可。
func evictOne[V any](m map[string]V, order []string) []string {
	for len(order) > 0 {
		k := order[0]
		order = order[1:]
		if _, ok := m[k]; ok {
			delete(m, k)
			return order
		}
	}
	return order
}

// statsSnapshot 返回当前拒绝计数（加锁读，供宿主导出指标）。
func (q *quota) statsSnapshot() QuotaStats {
	if q == nil {
		return QuotaStats{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

// QuotaStats 返回三层配额的自打开服务以来的拒绝计数。
//
// 宿主可据此导出 bot_messages_throttled_total{layer}：
// 指标一涨就说明"不是平台在限我们，而是我们自己拦住了"。
func (c *Client) QuotaStats() QuotaStats { return c.quota.statsSnapshot() }
