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

	"github.com/kissqianfu/QQ-SDK/common/version"
	"github.com/kissqianfu/QQ-SDK/core/event"
	"github.com/kissqianfu/QQ-SDK/core/transport"
)

// userAgent 统一 UA，版本随 VERSION 走。
var userAgent = version.UserAgent()

const (
	// DefaultGatewayEndpoint 是获取 wss 地址的 HTTP 端点（2026-08 起官方统一域名；
	// 旧地址 https://api.sgroup.qq.com/gateway 仍可用，GatewayEndpoint 可覆盖）。
	DefaultGatewayEndpoint = "https://api.bot.qq.com/gateway"
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
	// maxStale 是默认读空闲判死阈值的上限（显式配置 StaleTimeout 不受此限）。
	maxStale = 5 * time.Minute
	// resumeWindow 是平台保留 Resume 补发能力的窗口：断线超过它就必须重新 Identify。
	resumeWindow = 10 * time.Second
	// writeTimeout 是单帧写超时（防半死 TCP 下写永久阻塞）。
	writeTimeout = 10 * time.Second
	// maxFrameBytes 是单帧读上限。
	maxFrameBytes = 8 << 20
)

// 控制帧错误：交给 Run 的重连循环决定下一步。
var (
	errReconnect      = errors.New("ws: 平台要求重连")
	errInvalidSession = errors.New("ws: session 失效")
	// ErrTerminalClose 表示平台以"不可恢复"关闭码断开；Run 直接返回，宿主应告警或退出。
	ErrTerminalClose = errors.New("ws: 网关关闭码不可恢复，停止重连")
)

// Intent 位（官方 intents；本 SDK 只涉及群聊/单聊相关位）。
const (
	IntentGuilds           uint32 = 1 << 0  // 频道（本 SDK 不支持）
	IntentGuildMembers     uint32 = 1 << 1  // 频道成员（本 SDK 不支持）
	IntentGroupMemberEvent uint32 = 1 << 24 // GROUP_MEMBER_ADD / GROUP_MEMBER_REMOVE / GROUP_JOIN_REQUEST
	IntentGroupAndC2CEvent uint32 = 1 << 25 // C2C_MESSAGE_CREATE / GROUP_AT_MESSAGE_CREATE / GROUP_MESSAGE_CREATE 等
	IntentInteraction      uint32 = 1 << 26 // INTERACTION_CREATE
)

// DefaultIntents 是 Options.Intents 为 0 时的默认订阅：群成员事件 + 群/单聊消息 + 互动。
// 若未授予群成员事件能力，订阅会被 4014 断开（Run 返回 ErrTerminalClose），
// 此时宿主应显式调小 Intents。
const DefaultIntents = IntentGroupMemberEvent | IntentGroupAndC2CEvent | IntentInteraction

// wsCloseError 携带网关关闭码（gorilla 的 *websocket.CloseError 提取而来）。
type wsCloseError struct {
	code int
	text string
}

// Error 实现 error。
func (e *wsCloseError) Error() string {
	return fmt.Sprintf("ws: 网关关闭 code=%d text=%q", e.code, e.text)
}

// closeAction 是关闭码的处置策略。
type closeAction int

const (
	closeReconnect       closeAction = iota // 可重连（可 Resume）
	closeResumeForbidden                    // 可重连，但必须清 session 后重新 Identify
	closeTerminal                           // 不可恢复，Run 直接返回 ErrTerminalClose
)

// classifyClose 按官方《WebSocket 错误码》表判定关闭码处置。
// 终态：4001/4002（协议 bug）、4003~4005（鉴权失败/未鉴权/重复鉴权）、
// 4010~4014（配置态错误）、4914/4915（下架/封禁）；
// 清 session 重连：4006/4007、4900~4913；其余按可重连处理。
func classifyClose(code int) closeAction {
	switch code {
	case 4001, 4002, 4003, 4004, 4005, 4010, 4011, 4012, 4013, 4014, 4914, 4915:
		return closeTerminal
	case 4006, 4007, 4900, 4901, 4902, 4903, 4904, 4905, 4906, 4907, 4908, 4909, 4910, 4911, 4912, 4913:
		return closeResumeForbidden
	default:
		return closeReconnect
	}
}

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

// SessionStartLimit 是平台对 session 创建的限制（官方 GET /gateway/bot 响应字段）。
type SessionStartLimit struct {
	Total     int `json:"total"`
	Remaining int `json:"remaining"`
	// ResetAfter 是限额重置剩余时间（毫秒）。
	ResetAfter     int `json:"reset_after"`
	MaxConcurrency int `json:"max_concurrency"`
}

// GatewayBotInfo 是 GET /gateway/bot 的响应：接入点 + 平台建议分片数 + session 限额。
type GatewayBotInfo struct {
	URL               string            `json:"url"`
	Shards            int               `json:"shards"`
	SessionStartLimit SessionStartLimit `json:"session_start_limit"`
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
	// GatewayBot 为 true 时改用 GET /gateway/bot（可拿分片建议与 session 限额）。
	GatewayBot bool
	HTTP       *http.Client
	Dialer     *websocket.Dialer
	// HeartbeatInterval 为 0 时用 Hello 下发的值。
	HeartbeatInterval time.Duration
	// StaleTimeout 是读空闲判死阈值；0 = 默认 2×心跳间隔（下限 minStaleGrace）。
	StaleTimeout time.Duration
	// Dedupe 是事件幂等钩子：返回 false 表示重复事件，直接跳过。
	Dedupe func(ctx context.Context, eventID string) (bool, error)
}

// Transport 实现 transport.Transport。
type Transport struct {
	opts Options
	// resumeUntil 是本次断线后仍允许 Resume 的截止时间（零值表示当前连接正常）。
	resumeUntil time.Time
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
		opts.Intents = DefaultIntents
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
	t := &Transport{opts: opts}
	return t, nil
}

// Name 实现 transport.Transport。
func (t *Transport) Name() string { return "websocket" }

// Run 阻塞运行：断线自动重连（10s 内 Resume，超出窗口重新 Identify）。
// 退避按 1s→2s→…→30s 指数递增；上一次连接稳定存活超过 healthyConn 时退避重置。
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
		// 下游明确关闭时不要再退避重连：否则 Close 之后还会建立一条新连接。
		if errors.Is(err, transport.ErrClosed) {
			t.opts.Logger.Info("ws 下游已关闭，停止重连")
			return nil
		}
		if err == nil {
			// connectOnce 正常返回（例如平台要求重连）→ 已无继续运行的必要。
			return nil
		}
		t.opts.Logger.Warn("ws 连接中断，准备重连", "err", err, "alive", time.Since(started).Round(time.Second))
		// 关闭码决定"能不能重连、要不要丢 session"，不能把所有断开都当网络抖动。
		var ce *wsCloseError
		if errors.As(err, &ce) {
			switch classifyClose(ce.code) {
			case closeTerminal:
				t.opts.Logger.Error("ws 网关以不可恢复关闭码断开，停止重连",
					"code", ce.code, "text", ce.text)
				return fmt.Errorf("%w: code=%d text=%q", ErrTerminalClose, ce.code, ce.text)
			case closeResumeForbidden:
				t.opts.Logger.Warn("ws 会话不可 Resume，清理 session 后重新 Identify",
					"code", ce.code, "text", ce.text)
				t.clearSession(ctx)
				t.resumeUntil = time.Time{}
			}
		}
		// 首次断线开始计时：平台只保留很短的重连补发窗口，超出就必须 Identify。
		if t.resumeUntil.IsZero() {
			t.resumeUntil = time.Now().Add(resumeWindow)
		}
		// 健康判断放在等待之前：稳定连接断开（偶发）时退避立即重置。
		if time.Since(started) >= healthyConn {
			backoff = initBackoff
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if time.Since(started) < healthyConn && backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// gatewayURL 先 GET /gateway 换 wss 地址。
func (t *Transport) gatewayURL(ctx context.Context, token string) (string, error) {
	info, err := t.getGateway(ctx, token, t.opts.GatewayEndpoint)
	if err != nil {
		return "", err
	}
	return info.URL, nil
}

// GatewayBot 取 GET /gateway/bot（分片建议 + session 创建限额），供宿主启动前自检。
// 宿主可据此决定 Shard.Count，或至少把 session_start_limit 打进日志/指标。
func (t *Transport) GatewayBot(ctx context.Context) (*GatewayBotInfo, error) {
	token, err := t.opts.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("ws: get token: %w", err)
	}
	return t.getGateway(ctx, token, t.opts.GatewayEndpoint+"/bot")
}

// getGateway 请求一个 gateway 端点并解析 url（/gateway 与 /gateway/bot 共用的骨架）。
func (t *Transport) getGateway(ctx context.Context, token, endpoint string) (*GatewayBotInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("ws: build gateway request: %w", err)
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := t.opts.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ws: get gateway: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ws: get gateway status %d", resp.StatusCode)
	}
	var payload GatewayBotInfo
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("ws: decode gateway: %w", err)
	}
	if payload.URL == "" {
		return nil, fmt.Errorf("ws: gateway url 为空")
	}
	return &payload, nil
}

// connectOnce 建一次连接并读到断开为止。
func (t *Transport) connectOnce(ctx context.Context, emit func(*event.Event) error) error {
	token, err := t.opts.Token(ctx)
	if err != nil {
		return fmt.Errorf("ws: get token: %w", err)
	}
	url := t.opts.GatewayURL
	if url == "" {
		if t.opts.GatewayBot {
			info, err := t.getGateway(ctx, token, t.opts.GatewayEndpoint+"/bot")
			if err != nil {
				return err
			}
			url = info.URL
			if info.Shards > 0 && t.opts.Shard.Count == 1 && info.Shards > 1 {
				t.opts.Logger.Warn("平台建议的分片数大于当前配置，单分片可能承载不稳",
					"recommended_shards", info.Shards, "configured", t.opts.Shard.Count)
			}
			if info.SessionStartLimit.Total > 0 {
				t.opts.Logger.Info("gateway session 限额",
					"total", info.SessionStartLimit.Total,
					"remaining", info.SessionStartLimit.Remaining,
					"reset_after_ms", info.SessionStartLimit.ResetAfter)
			}
		} else {
			url, err = t.gatewayURL(ctx, token)
			if err != nil {
				return err
			}
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

	// 读帧上限：防异常大帧打爆内存（网关正常事件远小于此）。
	conn.SetReadLimit(maxFrameBytes)

	var writeMu sync.Mutex
	write := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteJSON(v); err != nil {
			// 写失败通常意味着连接半死：主动关闭以打断读循环，尽快走重连。
			_ = conn.Close()
			return err
		}
		return nil
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
		return readError(err)
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

	// 读空闲判死阈值：超过没读到任何帧就主动断开重连。
	stale := t.opts.StaleTimeout
	if stale <= 0 {
		stale = 2 * interval
		// 下限按"一个心跳周期 + 宽限"取，不能只跟 10s 比：心跳间隔很大时
		// 固定下限会让判死阈值小于一个心跳周期，连接必然被误杀。
		if floor := interval + minStaleGrace; stale < floor {
			stale = floor
		}
		// 上限只压缩"默认放大"，且不得小于一个心跳周期。
		if stale > maxStale && interval < maxStale {
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
	// 握手已发出：连接恢复，Resume 窗口重新计时。
	t.resumeUntil = time.Time{}

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

	var idWarned bool // 缺外层 id 只告警一次

	// 4) 读事件循环（每帧前重设读超时，靠 StaleTimeout 判死）。
	for {
		_ = conn.SetReadDeadline(time.Now().Add(stale))
		_, frameBytes, err := conn.ReadMessage()
		if err != nil {
			return readError(err)
		}
		var frame struct {
			Op int             `json:"op"`
			S  int64           `json:"s"`
			T  string          `json:"t"`
			ID string          `json:"id"`
			D  json.RawMessage `json:"d"`
		}
		if err := json.Unmarshal(frameBytes, &frame); err != nil {
			t.opts.Logger.Warn("ws 事件帧 JSON 非法（跳过）", "err", err)
			continue
		}
		switch frame.Op {
		case 0: // 事件分发
			if frame.ID == "" && !idWarned {
				// 外层 id 缺失时去重与 event_id 被动回复都失效，一次性提醒（每连接一次）。
				idWarned = true
				t.opts.Logger.Warn("ws 事件帧缺少外层 id：去重与 event_id 被动回复将不可用", "type", frame.T)
			}
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
			// 把网关帧组装成 Envelope 直接走 ParseEnvelope：省掉「marshal 回 JSON
			// 再整体 unmarshal」的一轮重复编解码。
			env := event.Envelope{ID: frame.ID, Op: frame.Op, S: frame.S, T: frame.T, D: frame.D}
			ev, err := event.ParseEnvelope(&env)
			if err != nil {
				// 坏帧不重投：先持久化 seq 再跳过，否则每次重连都会卡在同一帧。
				t.saveSession(ctx, snapshot)
				t.opts.Logger.Warn("ws 事件解析失败（保留连接）", "err", err, "type", frame.T)
				continue
			}
			if t.opts.Dedupe != nil && ev.ID != "" {
				first, dedupeErr := t.opts.Dedupe(ctx, ev.ID)
				if dedupeErr != nil {
					t.opts.Logger.Warn("ws 事件去重失败（继续处理）", "err", dedupeErr, "event_id", ev.ID)
				} else if !first {
					t.saveSession(ctx, snapshot)
					continue
				}
			}
			// 必须在投递成功之后再持久化 seq：先落盘会让 emit 失败的那一条
			// 在 Resume 时被当作"已消费"跳过，造成永久丢事件。
			if err := emit(ev); err != nil {
				return err
			}
			t.saveSession(ctx, snapshot)
		case 7: // 平台要求重连
			return errReconnect
		case 9: // session 失效：清掉，下次重新 Identify
			t.clearSession(ctx)
			return errInvalidSession
		default: // 10 Hello / 11 心跳 ACK / 其它控制帧
			continue
		}
	}
}

// readError 把 gorilla 的关闭帧错误归一成带 code 的 wsCloseError（其余保持原样包装）。
func readError(err error) error {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return &wsCloseError{code: ce.Code, text: ce.Text}
	}
	return fmt.Errorf("ws: read frame: %w", err)
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

// loadSession 取持久化会话；超出 Resume 窗口则主动清掉，改走 Identify。
func (t *Transport) loadSession(ctx context.Context) (Session, bool) {
	if t.opts.SessionStore == nil {
		return Session{}, false
	}
	if !t.resumeUntil.IsZero() && time.Now().After(t.resumeUntil) {
		t.opts.Logger.Warn("ws 断线已超出 Resume 窗口，清理 session 后重新 Identify")
		t.clearSession(ctx)
		t.resumeUntil = time.Time{}
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
