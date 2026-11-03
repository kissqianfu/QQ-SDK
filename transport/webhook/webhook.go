// Package webhook 是 QQ 回调接入传输：Ed25519 验签、op13 挑战、op12 ACK。
//
// SDK 只暴露 http.Handler，不自己 ListenAndServe：端口/TLS/反代由业务决定。
// 业务用法：
//
//	h, _ := webhook.New(webhook.Options{Secret: appSecret, OnEvent: handler})
//	mux.Handle("/aichat/webhook", h)
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
	"sync/atomic"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
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
	// queueSize 是异步派发队列容量：ACK 之后平台不会重推，队列比旧信号量模型更能吸收突发。
	queueSize = 256
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
	//
	// ⚠️ ACK 先于处理发出，平台不会重推；队列满时事件会被丢弃并计入 Dropped()。
	// OnEvent 慢（例如同步写库/调外部接口）的宿主应调大 QueueSize/Workers，
	// 或把 OnEvent 里再做一次"入自己的持久队列"，避免突发下静默丢事件。
	QueueSize int
	Workers   int

	// Deduper 可选：返回 false 表示重复（只 ACK、不回调）。
	Deduper func(ctx context.Context, meta EventMeta) (bool, error)
	// OnDrop 可选：事件因队列满被丢弃时回调（ACK 已发出、平台不会重推，属不可恢复丢事件）。
	// 宿主应在这里落死信表/打点告警；注意回调在 HTTP 请求 goroutine 上执行，必须快速返回。
	OnDrop func(meta EventMeta)
	// OnEvent 收到已验签事件后调用（ACK 之后异步执行）。
	OnEvent func(ctx context.Context, ev *event.Event)
}

// Handler 是 webhook http.Handler。
type Handler struct {
	opts    Options
	key     ed25519.PrivateKey
	now     func() time.Time
	queue   chan []byte // 已 ACK 待异步处理的事件体
	log     *slog.Logger
	acked   atomic.Int64
	dropped atomic.Int64
}

// seedFromSecret 按官方协议把 Bot Secret repeat 到 32 字节，作为 Ed25519 seed。
//
// 官方要求：secret 不足 32 字节时重复拼接（不是零填充），否则签名的公私钥与平台不一致。
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
	seed := seedFromSecret(opts.Secret)
	h := &Handler{
		opts:  opts,
		key:   ed25519.NewKeyFromSeed(seed),
		now:   time.Now,
		queue: make(chan []byte, opts.QueueSize),
		log:   opts.Logger,
	}
	if opts.OnEvent != nil {
		for range opts.Workers {
			go h.worker()
		}
	}
	return h, nil
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

// ServeHTTP 实现 http.Handler。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		if got := r.Header.Get(AppIDHeader); got != "" && got != h.opts.AppID {
			h.log.Warn("webhook appid 不匹配", "got", got)
			http.Error(w, "appid mismatch", http.StatusUnauthorized)
			return
		}
	}

	meta := EventMeta{ID: env.ID, Type: env.T, Raw: body}
	if h.opts.Deduper != nil && meta.ID != "" {
		first, err := h.opts.Deduper(r.Context(), meta)
		if err != nil {
			h.log.Error("webhook 幂等写入失败", "event_id", meta.ID, "err", err)
			http.Error(w, "dedupe failed", http.StatusInternalServerError)
			return
		}
		if !first {
			h.writeACK(w)
			return
		}
	}

	// 先 ACK，再异步解析/回调：平台 3s 超时会重推。
	h.writeACK(w)
	h.dispatch(body)
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

// withinWindow 判断 unix 秒是否落在 ±Window 内。
//
// ⚠️ op0 验签与 op13 挑战【必须共用这一份判断】：两处各写一遍的话，
// 改窗口时漏改一处，就会出现"挑战能签、事件收不下"或反之的诡异状态。
func (h *Handler) withinWindow(ts int64) bool {
	delta := h.now().Sub(time.Unix(ts, 0))
	return delta <= h.opts.Window && delta >= -h.opts.Window
}

// isPlainToken 校验 op13 的 plain_token 是否为官方形态（32 位十六进制）。
//
// ⚠️ 这不是格式洁癖，是【安全边界】：op0 的验签内容是 timestamp+body，
// 而 op13 会对 event_ts+plain_token 无条件出签名。若不限 token 形态，
// 攻击者把任意事件体塞进 plain_token 就能拿到它的合法签名（签名预言机），
// 再以同一个 event_ts 作 X-Signature-Timestamp、该 token 作 body 发 op0，
// 验签必然通过 ⇒ 可伪造任意入站事件（例如 GroupDelRobot 解绑告警群）。
// 把被签名的内容锁死在"纯数字时间戳 + 字母数字 token"之后，它不可能构成 JSON 事件信封
// （JSON 必然含 { } " : ，这些字符都被拒）。
//
// 为什么不锁 32 位 hex：官方文档的回调验证示例里 plain_token 是 20 位字母数字
// （"Arq0D5A61EgUu4OxUvOp"），把口径卡成 32 位 hex 会把平台自己的挑战全部拒掉，
// 回调地址将永远验证不通过；8~64 位字母数字同时覆盖官方示例与历史 32 位 hex 形态。
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
//
// ⚠️ 这是全流程里唯一"不验签就给签名"的入口，因此把入参卡到最窄形态：
// 时间戳必须是数字且在窗口内、plain_token 必须是 8~64 位字母数字（见 isChallengeToken）。
// 任何一项不合法都直接 400，绝不产出签名。
func (h *Handler) handleChallenge(w http.ResponseWriter, r *http.Request, d json.RawMessage) {
	// appid 校验与 op0 同口径：只在配置了期望值且请求带了头时比对。
	if h.opts.AppID != "" {
		if got := r.Header.Get(AppIDHeader); got != "" && got != h.opts.AppID {
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

// rejectChallenge 统一拒绝 op13。
//
// ⚠️ 响应只给通用原因、日志只记失败原因短语，【不回显 plain_token / event_ts】：
// body 上限 1MB，回显等于让任何人把任意载荷写进日志。
func (h *Handler) rejectChallenge(w http.ResponseWriter, reason string) {
	h.log.Warn("op13 挑战被拒绝", "reason", reason)
	http.Error(w, "invalid challenge", http.StatusBadRequest)
}

// dispatch 入队异步解析并回调事件；队列满时丢弃并计数（ACK 已发出，平台不会重推）。
func (h *Handler) dispatch(body []byte) {
	if h.opts.OnEvent == nil {
		return
	}
	select {
	case h.queue <- body:
	default:
		// ⚠️ ACK 已发出，平台不会重推，这里丢事件是**不可恢复**的。
		// 调用 OnDrop 让宿主落死信/告警（默认只计数 + 一条 Warn）。
		h.dropped.Add(1)
		meta := EventMeta{Raw: body}
		var env event.Envelope
		if err := json.Unmarshal(body, &env); err == nil {
			meta.ID, meta.Type = env.ID, env.T
		}
		h.log.Warn("webhook 处理队列已满，丢弃事件（不可恢复，请调大 QueueSize/Workers 或改异步落库）",
			"event_id", meta.ID, "type", meta.Type, "dropped_total", h.dropped.Load())
		if h.opts.OnDrop != nil {
			h.opts.OnDrop(meta)
		}
	}
}

// worker 消费派发队列：解析事件并回调；业务回调 panic 不得打崩进程。
func (h *Handler) worker() {
	for body := range h.queue {
		h.handleBody(body)
	}
}

func (h *Handler) handleBody(body []byte) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("webhook OnEvent panic（已恢复）", "panic", r)
		}
	}()
	ev, err := event.Parse(body)
	if err != nil {
		h.log.Warn("webhook 事件解析失败", "err", err)
		return
	}
	h.opts.OnEvent(context.Background(), ev)
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
