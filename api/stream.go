package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
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

// StreamReply 是一条进行中的单聊流式消息（官方"流式发送单聊消息"）。
//
// 用法（LLM delta 流 → 打字机效果）：
//
//	st, err := c.StartStream(ctx, api.Target{Scene: event.SceneC2C, ID: userOpenID},
//	    "正在生成回答，请稍候", api.StreamConfig{MsgID: ev.MsgID, ContentType: api.StreamMarkdown})
//	for delta := range llmCh {
//	    st, err = st.AppendDelta(ctx, delta) // 或 st.ReplaceFull(ctx, full) 做纠错
//	}
//	st.Finish(ctx)
//
// 线程约束：同一时间只允许一个 goroutine 驱动一条流（平台按 index 串行合并）。
type StreamReply struct {
	c      *Client
	userID string
	cfg    StreamConfig

	id    string // stream_msg_id
	index int    // 下一帧序号（从 0 递增）
	sent  string // 已下发全文（replace 模式本地前缀校验用）
	done  bool
}

// StartStream 开启一条流式消息并发送首片（index=0，input_state=1）。
//
// 仅支持单聊场景（群聊平台无流式接口，传群 Target 直接报错）。
// content 是首片内容，可为空串（仅占位，让客户端立刻出消息）。
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
	// 归一化结果必须写回 cfg：frame() 用的是 s.cfg.ContentType，
	// 否则空串会原样进请求体（平台按"显式空字符串"解析，不是"未传"）。
	cfg.ContentType = ct
	if cfg.MsgSeq == 0 {
		cfg.MsgSeq = 1
	}
	if target.ID == "" {
		return nil, fmt.Errorf("api: user_openid 不能为空")
	}

	s := &StreamReply{c: c, userID: target.ID, cfg: cfg}
	if _, err := s.frame(ctx, streamStateGenerating, streamModeAppend, content); err != nil {
		return nil, err
	}
	return s, nil
}

// AppendDelta 以增量模式追加一段内容（content_raw 拼接到平台侧 Pending）。
// LLM 的 delta 直接喂进来即可。
func (s *StreamReply) AppendDelta(ctx context.Context, delta string) (*StreamFrame, error) {
	return s.frame(ctx, streamStateGenerating, streamModeAppend, delta)
}

// ReplaceFull 以全量模式替换正文：full 必须以已下发内容为前缀
// （本地先校验，前缀被改时省一次 40007 往返）。适合需要纠错/重写的场景。
func (s *StreamReply) ReplaceFull(ctx context.Context, full string) (*StreamFrame, error) {
	if s.sent != "" && !strings.HasPrefix(full, s.sent) {
		return nil, fmt.Errorf("api: 流式内容与已下发前缀不一致（平台 40007：已下发内容前缀不可修改）")
	}
	return s.frame(ctx, streamStateGenerating, streamModeReplace, full)
}

// Finish 结束流式（input_state=10）。结束后的句柄不可再用（幂等安全：返回已结束错误）。
func (s *StreamReply) Finish(ctx context.Context) (*StreamFrame, error) {
	return s.frame(ctx, streamStateDone, streamModeAppend, "")
}

// ID 返回 stream_msg_id（首片发出后可用）。
func (s *StreamReply) ID() string { return s.id }

// frame 发送一帧并推进句柄状态。
func (s *StreamReply) frame(ctx context.Context, state int, mode, content string) (*StreamFrame, error) {
	if s.done {
		return nil, errors.New("api: 流式消息已结束")
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

	path, err := scenePath(event.SceneC2C, s.userID, "/stream_messages")
	if err != nil {
		return nil, err
	}
	tok, err := s.c.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := s.c.do(ctx, http.MethodPost, s.c.baseURL+path, tok, body)
	if err != nil {
		return nil, fmt.Errorf("api: stream frame %d: %w", s.index, err)
	}
	res, err := parseSendResult(raw)
	if err != nil {
		return nil, err
	}
	if res.ID == "" {
		return nil, fmt.Errorf("api: 流式响应缺少 stream_msg_id")
	}
	var extra struct {
		RemainMsgLen int `json:"remain_msg_len"`
	}
	_ = json.Unmarshal(raw, &extra)

	s.id = res.ID
	s.index++
	switch mode {
	case streamModeReplace:
		s.sent = content
	case streamModeAppend:
		s.sent += content
	}
	s.done = state == streamStateDone
	return &StreamFrame{
		ID:           res.ID,
		Timestamp:    res.Timestamp,
		RefIdx:       res.RefIdx,
		RemainMsgLen: extra.RemainMsgLen,
	}, nil
}
