package api

import (
	"sync"
	"time"

	"github.com/kissqianfu/QQ-SDK/common/apierr"
	"github.com/kissqianfu/QQ-SDK/common/qqtypes"
	"github.com/kissqianfu/QQ-SDK/core/event"
)

// 本文件是出站主动消息三层配额的唯一实现（全局桶 / 单关系桶 / 日上限）。
// 纯内存 best-effort 自保，权威口径永远是平台返回的 429；被本地拒绝时不调用平台。

// 默认口径。
const (
	defaultGroupQPM    = 60
	defaultSingleQPS   = 10
	defaultRelationQPM = 20
	defaultDailyLimit  = 1000
	// keyCap 是单关系桶与日计数表的默认键上限（KeyCap 可覆盖），超出按 FIFO 淘汰。
	keyCap = 4096
)

// quotaLocation 是日上限的日界时区（官方口径 UTC+8）。
var quotaLocation = time.FixedZone("UTC+8", 8*3600)

// QuotaConfig 是三层配额参数；0 = 取默认口径，负数 = 关闭对应层，Disable = true 整体关闭。
type QuotaConfig struct {
	Disable        bool
	GroupQPM       int            // 群聊全局：每分钟条数（默认 60）
	SingleQPS      int            // 单聊全局：每秒条数（默认 10）
	RelationQPM    int            // 单关系：每分钟条数（默认 20）
	DailyPerTarget int            // 每目标每日上限（默认 1000）
	Location       *time.Location // 日界时区（默认 UTC+8）
	// KeyCap 是键表上限（默认 4096），超出按 FIFO 淘汰最老的键。
	KeyCap int
	// Now 可注入时钟，测试用；nil 用 time.Now。
	Now func() time.Time
}

// normalize 填默认值：各层 0 = 取默认口径，负数 = 关闭该层（DailyPerTarget 负数 = 关闭日上限）。
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
	if c.KeyCap == 0 {
		c.KeyCap = keyCap
	}
	if c.Location == nil {
		c.Location = quotaLocation
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// QuotaStats 是三层各自的拒绝计数，供宿主导出指标。
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
	// 桶只在 rate > 0 时创建（newBucket 的两个入口都有正数校验）。
	need := (1 - b.tokens) / b.rate
	return false, time.Duration(need * float64(time.Second))
}

// refund 退回一个令牌（内层拒绝时把外层已扣的还回去）。
func (b *bucket) refund(now time.Time) {
	b.refill(now)
	b.tokens++
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

// quota 是三层配额的运行时状态。
type quota struct {
	cfg    QuotaConfig
	keyCap int

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
		keyCap:    c.KeyCap,
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

// acquire 为一条主动消息申请配额；被动回复不应调用。
//
// 先只查不消耗地看日上限，再扣两层桶令牌，全部通过后才计日数。
func (q *quota) acquire(scene event.Scene, targetID string) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.cfg.Now()
	q.rollDay(now)
	key := quotaKey(scene, targetID)

	// ① 日上限：只查不消耗，超了立刻拒（不白掉桶令牌）。
	if q.cfg.DailyPerTarget > 0 && q.daily[key] >= q.cfg.DailyPerTarget {
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
	if rb := q.relationBucket(key, now); rb != nil {
		ok, wait := rb.take(now)
		if !ok {
			if gb != nil {
				gb.refund(now)
			}
			q.stats.Relation++
			return &apierr.ThrottleError{Layer: "relation", RetryAfter: wait}
		}
	}

	// 日计数只有发送成功后才计（见 noteSent）。
	return nil
}

// noteSent 记一条成功发出的主动消息（日上限只统计成功发出的）。
func (q *quota) noteSent(scene event.Scene, targetID string) {
	if q == nil || q.cfg.DailyPerTarget <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollDay(q.cfg.Now())
	q.noteDaily(quotaKey(scene, targetID))
}

// refund 退回一次未送达主动消息所占用的令牌（日计数本来就没占，无需处理）。
// 调用时机：Token 失败、网络错误、5xx、429、4xx 都退；HTTP 2xx 之后不退。
func (q *quota) refund(scene event.Scene, targetID string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.cfg.Now()
	if scene == event.SceneGroup {
		if q.group != nil {
			q.group.refund(now)
		}
	} else {
		if q.single != nil {
			q.single.refund(now)
		}
	}
	// 单关系桶只在已存在时退（不为退款新建桶）。
	if b, ok := q.relations[quotaKey(scene, targetID)]; ok {
		b.refund(now)
	}
}

// quotaKey 把场景并进键：群 openid 与用户 openid 属于不同命名空间，
// 共用键会让两者的日上限与单关系桶互相挤占。
func quotaKey(scene event.Scene, targetID string) string {
	if scene == event.SceneGroup {
		return "group:" + targetID
	}
	return "user:" + targetID
}

// relationBucket 取（必要时创建）某个目标的桶；键表满时淘汰最老的键。
func (q *quota) relationBucket(id string, now time.Time) *bucket {
	if q.cfg.RelationQPM <= 0 {
		return nil
	}
	if b, ok := q.relations[id]; ok {
		return b
	}
	if len(q.relations) >= q.keyCap {
		q.relOrder = qqtypes.EvictFIFO(q.relations, q.relOrder)
	}
	b := newBucket(float64(q.cfg.RelationQPM)/60, float64(q.cfg.RelationQPM), now)
	q.relations[id] = b
	q.relOrder = append(q.relOrder, id)
	return b
}

// noteDaily 记一次当日成功发送（调用前已确认过日上限）。
func (q *quota) noteDaily(id string) {
	if _, ok := q.daily[id]; !ok {
		if len(q.daily) >= q.keyCap {
			q.dailyKeys = qqtypes.EvictFIFO(q.daily, q.dailyKeys)
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

// statsSnapshot 返回当前拒绝计数（加锁读，供宿主导出指标）。
func (q *quota) statsSnapshot() QuotaStats {
	if q == nil {
		return QuotaStats{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

// QuotaStats 返回三层配额自服务启动以来的拒绝计数。
func (c *Client) QuotaStats() QuotaStats { return c.quota.statsSnapshot() }
