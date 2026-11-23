package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

// 流式消息内容类型（POST /v2/users/{id}/stream_messages 的 content_type）。
const (
	StreamText     = "text"
	StreamMarkdown = "markdown"
)

const (
	streamStateGenerating = 1  // input_state：生成中
	streamStateDone       = 10 // input_state：生成结束
	streamModeAppend      = "append"
	streamModeReplace     = "replace"
)

// StreamConfig 是开启流式消息的参数。
type StreamConfig struct {
	// MsgID / EventID 是被动回复锚点（二选一，与普通发消息规则一致）。
	MsgID   string
	EventID string
	// ContentType 是内容格式：StreamText / StreamMarkdown；空 = text。
	ContentType string
	// MsgSeq 用于去重；官方示例整条流式消息共用同一个值。空 = 1。
	MsgSeq int
	// IsWakeup 为召回消息：平台不校验 msg_id/event_id 有效期。
	IsWakeup bool
}

// StreamFrame 是一帧的响应。
type StreamFrame struct {
	// ID 是 stream_msg_id：首片由服务端生成，后续分片回传它。
	ID string
	// Timestamp 是发送时间。
	Timestamp time.Time
	// RefIdx 是引用索引（ext_info.ref_idx），可对这条流式消息本身做引用回复。
	RefIdx string
	// RemainMsgLen 是流式消息剩余可写字符数；接近 0 时应尽快 Finish 或截断。
	RemainMsgLen int
}

// StreamReply 是一条进行中的单聊流式消息（LLM delta 流 → 打字机效果）。
// 同一时间只允许一个 goroutine 驱动一条流。
type StreamReply struct {
	// sendMu 让帧串行（保顺序与 index 单调），且在网络请求期间不被 s.mu 挡住只读方法。
	sendMu sync.Mutex
	mu     sync.Mutex

	c      *Client
	userID string
	cfg    StreamConfig

	id    string          // stream_msg_id
	index int             // 下一帧序号（从 0 递增）
	sent  strings.Builder // 已下发全文（replace 模式本地前缀校验用）
	done  bool
}

// StartStream 开启一条流式消息并发送首片；仅支持单聊，content 可为空串占位。
func (c *Client) StartStream(ctx context.Context, target Target, content string, cfg StreamConfig) (*StreamReply, error) {
	if target.Scene != event.SceneC2C {
		return nil, fmt.Errorf("api: 流式消息仅支持单聊场景（平台不支持群聊流式）")
	}
	if cfg.MsgID != "" && cfg.EventID != "" {
		return nil, fmt.Errorf("api: msg_id 与 event_id 锚点只能二选一")
	}
	ct := cfg.ContentType
	if ct == "" {
		ct = StreamText
	}
	if ct != StreamText && ct != StreamMarkdown {
		return nil, fmt.Errorf("api: 未知流式内容类型 %q", ct)
	}
	// 归一化结果写回 cfg。
	cfg.ContentType = ct
	if cfg.MsgSeq == 0 {
		cfg.MsgSeq = 1
	}
	if target.ID == "" {
		return nil, fmt.Errorf("api: user_openid 不能为空")
	}

	// 流式消息与普通发消息走同一套主动消息配额：无锚点即主动消息（is_wakeup 同样消耗）。
	active := cfg.MsgID == "" && cfg.EventID == ""
	if active {
		if err := c.quota.acquire(target.Scene, target.ID); err != nil {
			return nil, err
		}
	}
	delivered := false
	if active {
		defer func() {
			if !delivered {
				c.quota.refund(target.Scene, target.ID)
			}
		}()
	}
	s := &StreamReply{c: c, userID: target.ID, cfg: cfg}
	if _, err := s.frame(ctx, streamStateGenerating, streamModeAppend, content); err != nil {
		return nil, err
	}
	delivered = true
	if active {
		c.quota.noteSent(target.Scene, target.ID)
	}
	return s, nil
}

// AppendDelta 以增量模式追加一段内容（content_raw 拼接到平台侧 Pending）。
// LLM 的 delta 直接喂进来即可。
func (s *StreamReply) AppendDelta(ctx context.Context, delta string) (*StreamFrame, error) {
	return s.frame(ctx, streamStateGenerating, streamModeAppend, delta)
}

// ReplaceFull 以全量模式替换正文：full 必须以已下发内容为前缀
// （平台 40007：已下发内容前缀不可修改，本地先校验）。
func (s *StreamReply) ReplaceFull(ctx context.Context, full string) (*StreamFrame, error) {
	if s.sent.Len() > 0 && !strings.HasPrefix(full, s.sent.String()) {
		return nil, fmt.Errorf("api: 流式内容与已下发前缀不一致（平台 40007：已下发内容前缀不可修改）")
	}
	return s.frame(ctx, streamStateGenerating, streamModeReplace, full)
}

// Finish 结束流式（input_state=10）。结束后的句柄不可再用（幂等安全：返回已结束错误）。
func (s *StreamReply) Finish(ctx context.Context) (*StreamFrame, error) {
	return s.frame(ctx, streamStateDone, streamModeAppend, "")
}

// ID 返回 stream_msg_id（首片发出后可用）。
func (s *StreamReply) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// frame 发送一帧并推进句柄状态：sendMu 串行化帧，s.mu 只用于读写状态，
// 网络请求在所有锁之外，慢请求不会卡住 ID()/Done()。
func (s *StreamReply) frame(ctx context.Context, state int, mode, content string) (*StreamFrame, error) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	body, index, err := s.frameBody(state, mode, content)
	if err != nil {
		return nil, err
	}
	path, err := scenePath(event.SceneC2C, s.userID, "/stream_messages")
	if err != nil {
		return nil, err
	}
	raw, err := s.c.doPath(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, fmt.Errorf("api: stream frame %d: %w", index, err)
	}
	res, err := parseSendResult(raw)
	if err != nil {
		return nil, err
	}
	if res.ID == "" {
		return nil, fmt.Errorf("api: 流式响应缺少 stream_msg_id")
	}
	return s.applyFrame(res, state, mode, content), nil
}

// frameBody 组这一帧的请求体并取出当前 index（持锁极短，纯内存）。
func (s *StreamReply) frameBody(state int, mode, content string) (map[string]any, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, 0, errors.New("api: 流式消息已结束")
	}
	body := map[string]any{
		"input_mode":   mode,
		"input_state":  state,
		"index":        s.index,
		"content_type": s.cfg.ContentType,
		"msg_seq":      s.cfg.MsgSeq,
	}
	if content != "" {
		body["content_raw"] = content
	}
	if s.cfg.MsgID != "" {
		body["msg_id"] = s.cfg.MsgID
	} else if s.cfg.EventID != "" {
		body["event_id"] = s.cfg.EventID
	}
	if s.id != "" {
		body["stream_msg_id"] = s.id
	}
	if s.cfg.IsWakeup {
		body["is_wakeup"] = true
	}
	return body, s.index, nil
}

// applyFrame 用响应推进状态机：id、index、已下发全文、done。
func (s *StreamReply) applyFrame(res *Result, state int, mode, content string) *StreamFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = res.ID
	s.index++
	switch mode {
	case streamModeReplace:
		s.sent.Reset()
		s.sent.WriteString(content)
	case streamModeAppend:
		s.sent.WriteString(content)
	}
	s.done = state == streamStateDone
	remain := 0
	if res.RemainMsgLen != nil {
		remain = *res.RemainMsgLen
	}
	return &StreamFrame{
		ID:           res.ID,
		Timestamp:    res.Timestamp,
		RefIdx:       res.RefIdx,
		RemainMsgLen: remain,
	}
}
