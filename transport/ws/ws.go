// Package ws 是 QQ 官方 WebSocket 网关传输：连接、心跳、断线重连、Resume。
//
// SDK 内部唯一允许引入第三方依赖的地方（gorilla/websocket）；业务侧只用 qqbot.Client。
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/transport"
)

const (
	// DefaultGatewayEndpoint 是获取 wss 地址的 HTTP 端点（官方接入指南口径；
	// 新文档的统一域名 https://api.bot.qq.com/gateway 同样可用，GatewayEndpoint 可覆盖）。
	DefaultGatewayEndpoint = "https://api.sgroup.qq.com/gateway"
	// userAgent 统一 UA。
	userAgent = "qqbot-sdk/1.0"
	// defaultHeartbeat 是 Hello 没给间隔时的兜底（30s）。
	defaultHeartbeat = 30 * time.Second
	// initBackoff 是重连退避基数。
	initBackoff = time.Second
	// maxBackoff 是重连退避上限。
	maxBackoff = 30 * time.Second
	// healthyConn 是"稳定连接"阈值：存活超过它之后的断线视为偶发，退避重置。
	healthyConn = 60 * time.Second
	// minStaleGrace 是默认读空闲判死阈值的下限（避免误杀慢心跳连接）。
	minStaleGrace = 10 * time.Second
	// maxStale 是默认读空闲判死阈值的上限（防 Hello 下发异常大的 heartbeat_interval
	// 导致半死连接数天探测不到；显式配置 StaleTimeout 不受此限）。
	maxStale = 5 * time.Minute
)

// 控制帧错误：交给 Run 的重连循环决定下一步。
var (
	errReconnect      = errors.New("ws: 平台要求重连")
	errInvalidSession = errors.New("ws: session 失效")
)

// Session 是可持久化的会话状态（用于 Resume）。
type Session struct {
	SessionID string
	Seq       int64
}

// SessionStore 持久化 ws_session / ws_seq（bot_state 表）；由业务注入。
type SessionStore interface {
	Load(ctx context.Context) (Session, bool, error)
	Save(ctx context.Context, s Session) error
	Clear(ctx context.Context) error
}

// Options 是 ws Transport 参数。
type Options struct {
	AppID   string
	Token   func(ctx context.Context) (string, error) // access_token（不含 "QQBot " 前缀）
	Intents uint32
	Shard   transport.Shard

	SessionStore SessionStore
	Logger       *slog.Logger

	// GatewayURL 为空时先 GET GatewayEndpoint 换取 wss 地址（测试可直接覆盖）。
	GatewayURL      string
	GatewayEndpoint string
	HTTP            *http.Client
	Dialer          *websocket.Dialer
	// HeartbeatInterval 为 0 时用 Hello 下发的值。
	HeartbeatInterval time.Duration
	// StaleTimeout 是读空闲判死阈值：超过该时长没读到任何帧就断开重连。
	// 0 = 默认 2×心跳间隔（下限 minStaleGrace）；测试可显式调小。
	StaleTimeout time.Duration
	// Dedupe 是事件幂等钩子：返回 false 表示重复事件，直接跳过（业务侧注入 EventRepo.MarkOnce）。
	Dedupe func(ctx context.Context, eventID string) (bool, error)
}

// Transport 实现 transport.Transport。
type Transport struct {
	opts Options
}

// New 构造 ws transport（不连接）。
func New(opts Options) (*Transport, error) {
	if opts.AppID == "" {
		return nil, fmt.Errorf("ws: AppID 不能为空")
	}
	if opts.Token == nil {
		return nil, fmt.Errorf("ws: Token 不能为空")
	}
	if opts.Intents == 0 {
		opts.Intents = 1<<25 | 1<<26
	}
	if opts.Shard.Count == 0 {
		opts.Shard = transport.Shard{Index: 0, Count: 1}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.GatewayEndpoint == "" {
		opts.GatewayEndpoint = DefaultGatewayEndpoint
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Dialer == nil {
		opts.Dialer = websocket.DefaultDialer
	}
	return &Transport{opts: opts}, nil
}

// Name 实现 transport.Transport。
func (t *Transport) Name() string { return "websocket" }

// Run 阻塞运行：断线自动重连（10s 内 Resume，之后重新 Identify）。
//
// 退避策略：连续失败按 1s→2s→…→30s 指数退避；
// 但如果上一次连接已稳定存活超过 healthyConn，说明是偶发断线而非持续故障，退避立即重置。
func (t *Transport) Run(ctx context.Context, emit func(*event.Event) error) error {
	backoff := initBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		err := t.connectOnce(ctx, emit)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			continue // connectOnce 只在 ctx 取消时返回 nil，此处兜底
		}
		t.opts.Logger.Warn("ws 连接中断，准备重连", "err", err, "alive", time.Since(started).Round(time.Second))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if time.Since(started) >= healthyConn {
			backoff = initBackoff
			continue
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// gatewayURL 先 GET /gateway 换 wss 地址。
func (t *Transport) gatewayURL(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.opts.GatewayEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("ws: build gateway request: %w", err)
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.opts.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ws: get gateway: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ws: get gateway status %d", resp.StatusCode)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("ws: decode gateway: %w", err)
	}
	if payload.URL == "" {
		return "", fmt.Errorf("ws: gateway url 为空")
	}
	return payload.URL, nil
}

// connectOnce 建一次连接并读到断开为止。
func (t *Transport) connectOnce(ctx context.Context, emit func(*event.Event) error) error {
	token, err := t.opts.Token(ctx)
	if err != nil {
		return fmt.Errorf("ws: get token: %w", err)
	}
	url := t.opts.GatewayURL
	if url == "" {
		url, err = t.gatewayURL(ctx, token)
		if err != nil {
			return err
		}
	}

	header := http.Header{"User-Agent": []string{userAgent}}
	conn, resp, err := t.opts.Dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("ws: dial: %w", err)
	}
	closeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close() // ctx 取消时打断阻塞读
		case <-closeDone:
		}
	}()
	defer close(closeDone)
	defer func() { _ = conn.Close() }()

	var writeMu sync.Mutex
	write := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}

	// 1) Hello（op=10）拿心跳间隔。
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello struct {
		Op int `json:"op"`
		D  struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		} `json:"d"`
	}
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("ws: read hello: %w", err)
	}
	if hello.Op != 10 {
		return fmt.Errorf("ws: 期望 hello(op=10)，实际 op=%d", hello.Op)
	}
	_ = conn.SetReadDeadline(time.Time{}) // 读超时改由读循环按帧重设（见下）

	interval := t.opts.HeartbeatInterval
	if interval <= 0 {
		interval = time.Duration(hello.D.HeartbeatInterval) * time.Millisecond
	}
	if interval <= 0 {
		interval = defaultHeartbeat
	}

	// 读空闲判死阈值：超过没读到任何帧（含心跳 ACK）就主动断开重连，
	// 避免 TCP 半死连接下读永久阻塞、心跳写又静默失败的场景。
	stale := t.opts.StaleTimeout
	if stale <= 0 {
		stale = 2 * interval
		if stale < minStaleGrace {
			stale = minStaleGrace
		}
		if stale > maxStale {
			stale = maxStale
		}
	}

	// 2) Identify 或 Resume。
	sess, resume := t.loadSession(ctx)
	if resume {
		if err := write(map[string]any{"op": 6, "d": map[string]any{
			"token": "QQBot " + token, "session_id": sess.SessionID, "seq": sess.Seq,
		}}); err != nil {
			return fmt.Errorf("ws: resume: %w", err)
		}
	} else {
		if err := write(map[string]any{"op": 2, "d": map[string]any{
			"token":      "QQBot " + token,
			"intents":    t.opts.Intents,
			"shard":      []int{t.opts.Shard.Index, t.opts.Shard.Count},
			"properties": map[string]string{"$os": runtime.GOOS, "$browser": "qqbot-sdk", "$device": "qqbot-sdk"},
		}}); err != nil {
			return fmt.Errorf("ws: identify: %w", err)
		}
	}

	// 3) 心跳协程（seq 有并发读写，统一走 seqFn）。
	var seqMu sync.Mutex
	seqFn := func() int64 {
		seqMu.Lock()
		defer seqMu.Unlock()
		return sess.Seq
	}
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go t.heartbeat(hbCtx, write, interval, seqFn)

	// 4) 读事件循环（每帧前重设读超时，靠 StaleTimeout 判死）。
	for {
		var frame struct {
			Op int             `json:"op"`
			S  int64           `json:"s"`
			T  string          `json:"t"`
			ID string          `json:"id"`
			D  json.RawMessage `json:"d"`
		}
		_ = conn.SetReadDeadline(time.Now().Add(stale))
		if err := conn.ReadJSON(&frame); err != nil {
			return fmt.Errorf("ws: read frame: %w", err)
		}
		switch frame.Op {
		case 0: // 事件分发
			seqMu.Lock()
			if frame.S != 0 {
				sess.Seq = frame.S
			}
			if frame.T == "READY" {
				var ready struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(frame.D, &ready); err == nil && ready.SessionID != "" {
					sess.SessionID = ready.SessionID
				}
			}
			snapshot := sess
			seqMu.Unlock()
			t.saveSession(ctx, snapshot)
			raw, err := buildEventJSON(frame.ID, frame.S, frame.T, frame.D)
			if err != nil {
				return err
			}
			ev, err := event.Parse(raw)
			if err != nil {
				t.opts.Logger.Warn("ws 事件解析失败（保留连接）", "err", err, "type", frame.T)
				continue
			}
			if t.opts.Dedupe != nil && ev.ID != "" {
				first, dedupeErr := t.opts.Dedupe(ctx, ev.ID)
				if dedupeErr != nil {
					t.opts.Logger.Warn("ws 事件去重失败（继续处理）", "err", dedupeErr, "event_id", ev.ID)
				} else if !first {
					continue
				}
			}
			if err := emit(ev); err != nil {
				return err
			}
		case 7: // 平台要求重连
			return errReconnect
		case 9: // session 失效：清掉，下次重新 Identify
			t.clearSession(ctx)
			return errInvalidSession
		case 10, 11: // Hello / 心跳 ACK
			continue
		default:
			continue
		}
	}
}

// heartbeat 每 interval 发一次 op=1（带最近 seq）。
func (t *Transport) heartbeat(ctx context.Context, write func(any) error, interval time.Duration, seq func() int64) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload := map[string]any{"op": 1, "d": nil}
			if s := seq(); s > 0 {
				payload["d"] = s
			}
			if err := write(payload); err != nil {
				return
			}
		}
	}
}

// loadSession 取持久化会话。
func (t *Transport) loadSession(ctx context.Context) (Session, bool) {
	if t.opts.SessionStore == nil {
		return Session{}, false
	}
	s, ok, err := t.opts.SessionStore.Load(ctx)
	if err != nil {
		t.opts.Logger.Warn("ws 读取 session 失败，走 Identify", "err", err)
		return Session{}, false
	}
	if !ok || s.SessionID == "" {
		return Session{}, false
	}
	return s, true
}

// saveSession 忽略持久化错误：Resume 失败会退回 Identify，不该打断连接。
func (t *Transport) saveSession(ctx context.Context, s Session) {
	if t.opts.SessionStore == nil || s.SessionID == "" {
		return
	}
	if err := t.opts.SessionStore.Save(ctx, s); err != nil {
		t.opts.Logger.Warn("ws 保存 session 失败", "err", err)
	}
}

// clearSession 清掉失效会话。
func (t *Transport) clearSession(ctx context.Context) {
	if t.opts.SessionStore == nil {
		return
	}
	if err := t.opts.SessionStore.Clear(ctx); err != nil {
		t.opts.Logger.Warn("ws 清理 session 失败", "err", err)
	}
}

// buildEventJSON 把网关帧还原成 event.Parse 需要的外层 JSON。
func buildEventJSON(id string, seq int64, typ string, d json.RawMessage) ([]byte, error) {
	frame := struct {
		ID string          `json:"id"`
		Op int             `json:"op"`
		S  int64           `json:"s"`
		T  string          `json:"t"`
		D  json.RawMessage `json:"d"`
	}{ID: id, Op: 0, S: seq, T: typ, D: d}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("ws: build event: %w", err)
	}
	return raw, nil
}
