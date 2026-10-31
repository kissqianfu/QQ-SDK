package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/event"
	"github.com/kissqianfu/QQ-SDK/token"
)

// seededToken 预置有效 token：否则首次取 token 的请求也会打到 catch-all 测试服务器，
// 触发 input_mode 断言 panic → 5xx 无限重试 → 测试挂死到全局超时。
func seededToken(t *testing.T) token.Store {
	t.Helper()
	st := token.NewMemory()
	if err := st.Set(context.Background(), "tok-1", time.Hour); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return st
}

func TestStartStreamRejectsGroup(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, _ *http.Request) {}), seededToken(t))
	_, err := c.StartStream(context.Background(), Target{Scene: event.SceneGroup, ID: "g1"}, "hi", StreamConfig{MsgID: "m1"})
	if err == nil {
		t.Fatal("StartStream(group) error = nil, want 单聊限制错误")
	}
}

func TestStreamAnchorValidation(t *testing.T) {
	c := newClient(t, testServer(t, func(w http.ResponseWriter, _ *http.Request) {}), seededToken(t))
	_, err := c.StartStream(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"}, "",
		StreamConfig{MsgID: "m1", EventID: "e1"})
	if err == nil {
		t.Fatal("StartStream(msg_id+event_id) error = nil, want 二选一错误")
	}
}

func TestStreamLifecycle(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/users/u1/stream_messages" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		switch n {
		case 1:
			writeJSON(w, 200, `{"id":"stream-1","timestamp":"2026-07-21T10:00:00+08:00","ext_info":{"ref_idx":"REFIDX_X=="},"remain_msg_len":100}`)
		case 2:
			writeJSON(w, 200, `{"id":"stream-1","timestamp":"2026-07-21T10:00:01+08:00","remain_msg_len":50}`)
		case 3:
			writeJSON(w, 200, `{"id":"stream-1","timestamp":"2026-07-21T10:00:02+08:00","remain_msg_len":0}`)
		}
	})
	c := newClient(t, srv, seededToken(t))

	st, err := c.StartStream(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"}, "正在生成",
		StreamConfig{MsgID: "MSG-1", ContentType: StreamMarkdown})
	if err != nil {
		t.Fatalf("StartStream() error = %v", err)
	}
	if st.ID() != "stream-1" {
		t.Fatalf("ID = %q, want stream-1", st.ID())
	}

	if _, err := st.AppendDelta(context.Background(), "第一段"); err != nil {
		t.Fatalf("AppendDelta() error = %v", err)
	}
	fin, err := st.Finish(context.Background())
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if fin.ID != "stream-1" || fin.Timestamp.IsZero() {
		t.Fatalf("finish frame = %+v", fin)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("bodies = %d 帧", len(bodies))
	}
	// 首片：index=0、state=1、msg_id、默认 seq=1、content_type 透传。
	b0 := bodies[0]
	if b0["index"].(float64) != 0 || b0["input_state"].(float64) != 1 ||
		b0["msg_id"] != "MSG-1" || b0["msg_seq"].(float64) != 1 ||
		b0["content_type"] != StreamMarkdown || b0["content_raw"] != "正在生成" {
		t.Fatalf("首片 body = %+v", b0)
	}
	if _, has := b0["stream_msg_id"]; has {
		t.Fatal("首片不应携带 stream_msg_id")
	}
	// 续片：携带 stream_msg_id、index=1、append 模式。
	b1 := bodies[1]
	if b1["stream_msg_id"] != "stream-1" || b1["index"].(float64) != 1 ||
		b1["input_mode"] != "append" || b1["content_raw"] != "第一段" {
		t.Fatalf("续片 body = %+v", b1)
	}
	// 结束片：input_state=10，无 content_raw。
	b2 := bodies[2]
	if b2["input_state"].(float64) != 10 || b2["index"].(float64) != 2 {
		t.Fatalf("结束片 body = %+v", b2)
	}
	if _, has := b2["content_raw"]; has {
		t.Fatal("结束片不应有 content_raw")
	}

	// 结束后再追加必须报错。
	if _, err := st.AppendDelta(context.Background(), "late"); err == nil {
		t.Fatal("结束后 AppendDelta error = nil, want error")
	}
}

func TestStreamReplacePrefixCheck(t *testing.T) {
	var mu sync.Mutex
	var streamModes []string
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		streamModes = append(streamModes, body["input_mode"].(string))
		mu.Unlock()
		writeJSON(w, 200, `{"id":"stream-2","timestamp":"2026-07-21T10:00:00+08:00"}`)
	})
	c := newClient(t, srv, seededToken(t))

	st, err := c.StartStream(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"}, "abc",
		StreamConfig{EventID: "EV-1"})
	if err != nil {
		t.Fatalf("StartStream() error = %v", err)
	}
	// 前缀被改：本地拦截，不发请求。
	if _, err := st.ReplaceFull(context.Background(), "xyz"); err == nil {
		t.Fatal("ReplaceFull(prefix broken) error = nil, want 本地 40007 拦截")
	}
	// 合法前缀扩展：replace 通过，且已下发前缀随之更新。
	if _, err := st.ReplaceFull(context.Background(), "abcdef"); err != nil {
		t.Fatalf("ReplaceFull(extend) error = %v", err)
	}
	// event_id 锚点在请求体里（连续 replace 同前缀也放行）。
	if _, err := st.ReplaceFull(context.Background(), "abcdefg"); err != nil {
		t.Fatalf("ReplaceFull(same) error = %v", err)
	}
	mu.Lock()
	modes := append([]string(nil), streamModes...)
	mu.Unlock()
	if modes[0] != "append" || modes[1] != "replace" || modes[2] != "replace" {
		t.Fatalf("modes = %v", modes)
	}
}

func TestStreamFrameTimestampRFC3339(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"id":"s","timestamp":"2026-07-21T10:00:00+08:00","remain_msg_len":9}`)
	})
	c := newClient(t, srv, seededToken(t))
	st, err := c.StartStream(context.Background(), Target{Scene: event.SceneC2C, ID: "u1"}, "x",
		StreamConfig{MsgID: "m"})
	if err != nil {
		t.Fatalf("StartStream() error = %v", err)
	}
	// 首帧结果在 StartStream 内部消化；用 AppendDelta 验证 remain/timestamp 解析。
	frame, err := st.AppendDelta(context.Background(), "y")
	if err != nil {
		t.Fatalf("AppendDelta() error = %v", err)
	}
	want := time.Date(2026, 7, 21, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if !frame.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v", frame.Timestamp, want)
	}
	if frame.RemainMsgLen != 9 {
		t.Fatalf("RemainMsgLen = %d, want 9", frame.RemainMsgLen)
	}
}
