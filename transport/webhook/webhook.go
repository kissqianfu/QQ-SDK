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

	// Deduper 可选：返回 false 表示重复（只 ACK、不回调）。
	Deduper func(ctx context.Context, meta EventMeta) (bool, error)
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
	seed := seedFromSecret(opts.Secret)
	h := &Handler{
		opts:  opts,
		key:   ed25519.NewKeyFromSeed(seed),
		now:   time.Now,
		queue: make(chan []byte, queueSize),
		log:   opts.Logger,
	}
	if opts.OnEvent != nil {
		for range maxWorkers {
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
		h.handleChallenge(w, env.D)
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
	if delta := h.now().Sub(time.Unix(ts, 0)); delta > h.opts.Window || delta < -h.opts.Window {
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

// handleChallenge 处理 op13 URL 校验：签名内容 = event_ts + plain_token。
func (h *Handler) handleChallenge(w http.ResponseWriter, d json.RawMessage) {
	var payload struct {
		PlainToken string `json:"plain_token"`
		EventTS    string `json:"event_ts"`
	}
	if err := json.Unmarshal(d, &payload); err != nil {
		http.Error(w, "bad challenge payload", http.StatusBadRequest)
		return
	}
	if payload.PlainToken == "" || payload.EventTS == "" {
		http.Error(w, "op13 缺少 plain_token 或 event_ts", http.StatusBadRequest)
		return
	}
	sig := ed25519.Sign(h.key, []byte(payload.EventTS+payload.PlainToken))
	writeJSON(w, http.StatusOK, map[string]string{
		"plain_token": payload.PlainToken,
		"signature":   hex.EncodeToString(sig),
	})
}

// dispatch 入队异步解析并回调事件；队列满时丢弃并计数（ACK 已发出，平台不会重推）。
func (h *Handler) dispatch(body []byte) {
	if h.opts.OnEvent == nil {
		return
	}
	select {
	case h.queue <- body:
	default:
		h.dropped.Add(1)
		h.log.Warn("webhook 处理队列已满，丢弃事件")
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
