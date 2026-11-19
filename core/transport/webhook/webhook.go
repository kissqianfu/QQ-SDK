// Package webhook 是 QQ 回调接入传输：Ed25519 验签、op13 挑战、op12 ACK。
//
// 只暴露 http.Handler，端口/TLS/反代由业务决定。
package webhook

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

const (
	// SignatureHeader 是平台签名头。
	SignatureHeader = "X-Signature-Ed25519"
	// TimestampHeader 是平台签名时间戳头（秒）。
	TimestampHeader = "X-Signature-Timestamp"
	// AppIDHeader 是平台 appid 头（防回调填错到别的 Bot）。
	AppIDHeader = "X-Bot-Appid"

	// defaultWindow 默认时间窗（±5 分钟）。
	defaultWindow = 5 * time.Minute
	// defaultMaxBody 默认 body 上限（平台回调很小）。
	defaultMaxBody = 1 << 20
	// ackBody 是平台要求的 ACK。
	ackBody = `{"op":12,"d":0}`
	// maxWorkers 是异步回调 worker 数（资源上限）。
	maxWorkers = 32
	// queueSize 是异步派发队列容量（用于吸收突发；满了回 503 让平台重推）。
	queueSize = 256
	// defaultOnEventTimeout 是单条事件处理的默认超时：防止 OnEvent 卡死让 Close 永不返回。
	defaultOnEventTimeout = 30 * time.Second
)

// EventMeta 是去重所需的最少元数据（ID 用最外层 id，不是 d.id）。
type EventMeta struct {
	ID   string
	Type string
	Raw  []byte
}

// Options 是 webhook Handler 参数。
type Options struct {
	Secret  string // AppSecret（内部按官方协议 repeat 到 32 字节作为 Ed25519 seed）
	AppID   string // 可选：校验 X-Bot-Appid
	Window  time.Duration
	MaxBody int64
	Logger  *slog.Logger

	// QueueSize / Workers 是异步派发队列容量与消费并发（0 = 默认 256 / 32）。
	// 队列满时请求返回 503 让平台重推（不 ACK），只有入队成功才 ACK。
	QueueSize int
	Workers   int

	// Deduper 可选：返回 false 表示重复（只 ACK、不回调）。
	Deduper func(ctx context.Context, meta EventMeta) (bool, error)
	// OnDrop 可选：事件被丢弃时回调（在 HTTP 请求 goroutine 上执行，必须快速返回）。
	OnDrop func(meta EventMeta)
	// OnEventTimeout 是单条事件处理超时（0 = 默认 30s），只取消传给 OnEvent 的 ctx。
	OnEventTimeout time.Duration
	// OnEvent 收到已验签事件后调用（ACK 之后异步执行）。
	OnEvent func(ctx context.Context, ev *event.Event)
}

// Handler 是 webhook http.Handler。
type Handler struct {
	opts    Options
	key     ed25519.PrivateKey
	now     func() time.Time
	queue   chan queuedEvent // 已 ACK 待异步处理的事件（信封+原文成对入队，解析只做一次）
	log     *slog.Logger
	acked   atomic.Int64
	dropped atomic.Int64

	mu        sync.RWMutex
	closed    bool
	closedCh  chan struct{} // Close 完成（worker 全部退出）后关闭
	wg        sync.WaitGroup
	closeOnce sync.Once
	baseCtx   context.Context
	cancel    context.CancelFunc

	dropMu    sync.Mutex
	dropTypes map[string]int64
}

// seedFromSecret 按官方协议把 Secret repeat 到 32 字节作为 Ed25519 seed。
func seedFromSecret(secret string) []byte {
	seed := make([]byte, 0, ed25519.SeedSize)
	for len(seed) < ed25519.SeedSize {
		seed = append(seed, secret...)
	}
	return seed[:ed25519.SeedSize]
}

// New 构造 Handler（内部启动 maxWorkers 个常驻 worker 消费派发队列）。
func New(opts Options) (*Handler, error) {
	if opts.Secret == "" {
		return nil, fmt.Errorf("webhook: Secret 不能为空")
	}
	// OnEvent 缺失会让所有事件被 ACK 后静默丢弃，必须在构造期就拒绝。
	if opts.OnEvent == nil {
		return nil, fmt.Errorf("webhook: OnEvent 不能为空")
	}
	if opts.Window <= 0 {
		opts.Window = defaultWindow
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = defaultMaxBody
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = queueSize
	}
	if opts.Workers <= 0 {
		opts.Workers = maxWorkers
	}
	if opts.OnEventTimeout <= 0 {
		opts.OnEventTimeout = defaultOnEventTimeout
	}
	seed := seedFromSecret(opts.Secret)
	baseCtx, cancel := context.WithCancel(context.Background())
	h := &Handler{
		opts:      opts,
		key:       ed25519.NewKeyFromSeed(seed),
		now:       time.Now,
		queue:     make(chan queuedEvent, opts.QueueSize),
		log:       opts.Logger,
		dropTypes: make(map[string]int64),
		closedCh:  make(chan struct{}),
		baseCtx:   baseCtx,
		cancel:    cancel,
	}
	h.wg.Add(opts.Workers)
	for range opts.Workers {
		go h.worker()
	}
	return h, nil
}

// Close 停止接收新事件、关闭队列并等待 worker 处理完已入队事件；幂等且并发安全：
// 并发调用都会等到 worker 全部退出后才返回。
// 关闭后 ServeHTTP 返回 503，平台会重推（不静默丢事件）。
func (h *Handler) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		close(h.queue)
		h.mu.Unlock()
		h.wg.Wait()
		h.cancel()
		close(h.closedCh)
	})
	<-h.closedCh
}

// PublicKey 返回公钥（自检/测试用）。
func (h *Handler) PublicKey() ed25519.PublicKey {
	pub, _ := h.key.Public().(ed25519.PublicKey)
	return pub
}

// Acknowledged 返回已 ACK 次数（监控用）。
func (h *Handler) Acknowledged() int64 { return h.acked.Load() }

// Dropped 返回队列满被丢弃的事件数。
func (h *Handler) Dropped() int64 { return h.dropped.Load() }

// DroppedByType 返回按事件类型统计的丢弃数（用于区分严重程度的告警）。
func (h *Handler) DroppedByType() map[string]int64 {
	h.dropMu.Lock()
	defer h.dropMu.Unlock()
	out := make(map[string]int64, len(h.dropTypes))
	for k, v := range h.dropTypes {
		out[k] = v
	}
	return out
}

// ServeHTTP 实现 http.Handler。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		// Close 之后不再 ACK：让平台按重推策略重投（ACK 了却处理不了才是真丢）。
		http.Error(w, "webhook closed", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.opts.MaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}

	var env event.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if env.Op == 13 {
		h.handleChallenge(w, r, env.D)
		return
	}
	if env.Op != 0 {
		http.Error(w, "unsupported op", http.StatusBadRequest)
		return
	}

	if err := h.verify(r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader), body); err != nil {
		h.log.Warn("webhook 验签失败", "err", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if h.opts.AppID != "" {
		// 配了 AppID 就必须校验：允许缺失等于给"省略请求头"开了后门。
		if got := r.Header.Get(AppIDHeader); got != h.opts.AppID {
			h.log.Warn("webhook appid 不匹配", "got", got)
			http.Error(w, "appid mismatch", http.StatusUnauthorized)
			return
		}
	}

	// 入队成功才 ACK：队列满时回 503，让平台按重推策略重投，而不是 ACK 后丢事件。
	// 去重放在 worker 里做（见 handleBody）：在这里先提交去重记录，会让"入队失败"
	// 的事件被永久判重，连平台重推都救不回来。
	if !h.dispatch(queuedEvent{env: env, raw: body}) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	h.writeACK(w)
}

// queuedEvent 是入队事件：ServeHTTP 已解出的信封与原始 body 成对带走，
// worker 直接 ParseEnvelope，避免同一 payload 反复解码。
type queuedEvent struct {
	env event.Envelope
	raw []byte
}

// verify 校验 Ed25519(timestamp + body) 与时间窗。
func (h *Handler) verify(sigHex, timestamp string, body []byte) error {
	if sigHex == "" || timestamp == "" {
		return fmt.Errorf("webhook: 缺少签名头")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("webhook: 签名不是 hex: %w", err)
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("webhook: timestamp 非法: %w", err)
	}
	if !h.withinWindow(ts) {
		return fmt.Errorf("webhook: timestamp 超出 ±%s 窗口", h.opts.Window)
	}
	msg := make([]byte, 0, len(timestamp)+len(body))
	msg = append(msg, timestamp...)
	msg = append(msg, body...)
	if !ed25519.Verify(h.PublicKey(), msg, sig) {
		return fmt.Errorf("webhook: 签名校验失败")
	}
	return nil
}

// withinWindow 判断 unix 秒是否落在 ±Window 内（op0 验签与 op13 挑战共用）。
func (h *Handler) withinWindow(ts int64) bool {
	delta := h.now().Sub(time.Unix(ts, 0))
	return delta <= h.opts.Window && delta >= -h.opts.Window
}

// isChallengeToken 校验 op13 的 plain_token 是否为 8~64 位字母数字。
// 这是安全边界：锁死被签名内容的形态，防止签名预言机伪造事件。
func isChallengeToken(s string) bool {
	const minLen, maxLen = 8, 64
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// handleChallenge 处理 op13 URL 校验：签名内容 = event_ts + plain_token。
// 这是唯一不验签就出签名的入口，入参必须卡到最窄形态（见 isChallengeToken）。
func (h *Handler) handleChallenge(w http.ResponseWriter, r *http.Request, d json.RawMessage) {
	// appid 校验与 op0 同口径：只在配置了期望值且请求带了头时比对。
	if h.opts.AppID != "" {
		// 配了 AppID 就必须校验：允许缺失等于给"省略请求头"开了后门。
		if got := r.Header.Get(AppIDHeader); got != h.opts.AppID {
			h.log.Warn("op13 appid 不匹配", "got", got)
			http.Error(w, "appid mismatch", http.StatusUnauthorized)
			return
		}
	}

	var payload struct {
		PlainToken string `json:"plain_token"`
		EventTS    string `json:"event_ts"`
	}
	if err := json.Unmarshal(d, &payload); err != nil {
		http.Error(w, "bad challenge payload", http.StatusBadRequest)
		return
	}
	if !isChallengeToken(payload.PlainToken) {
		h.rejectChallenge(w, "plain_token 不是 8~64 位字母数字")
		return
	}
	ts, err := strconv.ParseInt(payload.EventTS, 10, 64)
	if err != nil {
		h.rejectChallenge(w, "event_ts 缺失或非数字")
		return
	}
	if !h.withinWindow(ts) {
		h.rejectChallenge(w, fmt.Sprintf("event_ts 超出 ±%s 窗口", h.opts.Window))
		return
	}

	sig := ed25519.Sign(h.key, []byte(payload.EventTS+payload.PlainToken))
	writeJSON(w, http.StatusOK, map[string]string{
		"plain_token": payload.PlainToken,
		"signature":   hex.EncodeToString(sig),
	})
}

// rejectChallenge 统一拒绝 op13（不回显 plain_token / event_ts，避免写日志）。
func (h *Handler) rejectChallenge(w http.ResponseWriter, reason string) {
	h.log.Warn("op13 挑战被拒绝", "reason", reason)
	http.Error(w, "invalid challenge", http.StatusBadRequest)
}

// dispatch 把事件放入异步队列；队列满或已关闭返回 false（调用方回 503 让平台重推）。
func (h *Handler) dispatch(item queuedEvent) bool {
	// RLock 与 Close 互斥：避免 Close 关队列时还在 send（panic: send on closed channel）。
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		h.recordDrop(item, "closed")
		return false
	}
	select {
	case h.queue <- item:
		return true
	default:
		h.recordDrop(item, "queue_full")
		return false
	}
}

// recordDrop 统一记账：计数 + 按类型分桶 + 日志 + OnDrop（队列满/已关闭共用）。
func (h *Handler) recordDrop(item queuedEvent, reason string) {
	h.dropped.Add(1)
	meta := EventMeta{Raw: item.raw, ID: item.env.ID, Type: item.env.T}
	h.log.Warn("webhook 事件被拒绝，平台将重推",
		"reason", reason, "event_id", meta.ID, "type", meta.Type, "dropped_total", h.dropped.Load())
	h.dropMu.Lock()
	if meta.Type == "" {
		h.dropTypes["unknown"]++
	} else {
		h.dropTypes[meta.Type]++
	}
	h.dropMu.Unlock()
	if h.opts.OnDrop != nil {
		h.opts.OnDrop(meta)
	}
}

// worker 消费派发队列：解析事件并回调；业务回调 panic 不得打崩进程。
func (h *Handler) worker() {
	defer h.wg.Done()
	for item := range h.queue {
		h.handleBody(item)
	}
}

func (h *Handler) handleBody(item queuedEvent) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("webhook OnEvent panic（已恢复）", "panic", r)
		}
	}()
	ev, err := event.ParseEnvelope(&item.env)
	if err != nil {
		// 解析失败同样不可恢复：走与队列丢弃相同的 OnDrop 通道。
		// 信封已在 ServeHTTP 解过，直接复用，不再第三次解码。
		h.recordDrop(item, "parse_error")
		h.log.Warn("webhook 事件解析失败（ACK 已发，不可恢复）", "err", err, "event_id", item.env.ID)
		return
	}
	// 去重必须发生在"确认事件已入队"之后：先提交去重再入队，一旦队列满，
	// 平台重推的同一事件会被判重直接吞掉。
	if h.opts.Deduper != nil && ev.ID != "" {
		meta := EventMeta{Raw: item.raw, ID: ev.ID, Type: item.env.T}
		first, err := h.opts.Deduper(h.baseCtx, meta)
		if err != nil {
			h.log.Error("webhook 幂等写入失败（继续处理）", "event_id", meta.ID, "err", err)
		} else if !first {
			return
		}
	}
	ctx, cancel := context.WithTimeout(h.baseCtx, h.opts.OnEventTimeout)
	defer cancel()
	h.opts.OnEvent(ctx, ev)
}

// writeACK 写平台 ACK。
func (h *Handler) writeACK(w http.ResponseWriter) {
	h.acked.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ackBody)
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
