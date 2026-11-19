package ws

// id_warn_test.go 钉住「缺外层 id」告警的判定口径：只看事件语义，不看帧本身。
// READY/RESUMED 没有外层 id 是协议常态；只有消息事件缺 id 才代表去重与被动回复失效。

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

// syncBuilder 让连接协程写日志与测试协程读日志并存。
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestMissingIDWarnOnlyForMessageEvents 校验三件事：
//  1. READY 没有外层 id 不告警（协议常态，不代表去重失效）；
//  2. 消息事件缺 id 告警，且同一类型在同一连接内只报一次；
//  3. 换一类消息事件再缺 id 时照常告警（不被前一条同连接告警吞掉）。
func TestMissingIDWarnOnlyForMessageEvents(t *testing.T) {
	g := newFakeGateway(t)
	logs := &syncBuilder{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	tr, err := New(Options{
		AppID:             "app",
		Token:             func(context.Context) (string, error) { return "tok", nil },
		GatewayURL:        g.wsURL(),
		Intents:           1 << 25,
		HeartbeatInterval: 30 * time.Millisecond,
		Logger:            logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	events, _, _ := runAndCancel(t, tr)

	// awaitEvents 等 n 个事件投递完成，避免断言时帧还没被读线程处理。
	awaitEvents := func(n int) {
		for i := 0; i < n; i++ {
			select {
			case <-events:
			case <-time.After(2 * time.Second):
				t.Fatalf("等第 %d 个事件超时，日志：\n%s", i+1, logs.String())
			}
		}
	}

	groupMsg := map[string]any{
		"id": "m-1", "content": "hi", "group_openid": "g-1",
		"author": map[string]any{"member_openid": "u-1"},
	}

	// 1) READY 无外层 id：不该告警。
	g.push(dispatchFrame(1, "READY", "", map[string]any{"session_id": "s-1"}))
	awaitEvents(1)
	if n := strings.Count(logs.String(), "缺少外层 id"); n != 0 {
		t.Fatalf("READY 缺 id 触发了 %d 次告警，want 0：\n%s", n, logs.String())
	}

	// 2) 群消息缺 id：告警一次；同类型再缺一次不重复。
	g.push(dispatchFrame(2, "GROUP_AT_MESSAGE_CREATE", "", groupMsg))
	awaitEvents(1)
	g.push(dispatchFrame(3, "GROUP_AT_MESSAGE_CREATE", "", groupMsg))
	awaitEvents(1)
	if n := strings.Count(logs.String(), "缺少外层 id"); n != 1 {
		t.Fatalf("同类消息缺 id 告警 = %d 次，want 1：\n%s", n, logs.String())
	}

	// 3) 另一类消息缺 id：单独告警（不能被上一类吞掉）。
	g.push(dispatchFrame(4, "C2C_MESSAGE_CREATE", "", map[string]any{
		"id": "m-2", "content": "hi", "author": map[string]any{"user_openid": "u-2"},
	}))
	awaitEvents(1)
	if n := strings.Count(logs.String(), "缺少外层 id"); n != 2 {
		t.Fatalf("第二类消息缺 id 后告警 = %d 次，want 2：\n%s", n, logs.String())
	}

	// 4) 带外层 id 的消息：不告警。
	g.push(dispatchFrame(5, "GROUP_AT_MESSAGE_CREATE", "EV-5", groupMsg))
	awaitEvents(1)
	if n := strings.Count(logs.String(), "缺少外层 id"); n != 2 {
		t.Fatalf("带 id 的消息触发了额外告警，总 = %d，want 2：\n%s", n, logs.String())
	}

	// 告警只应出现在消息事件上，且类型名可定位。
	for _, want := range []string{"type=GROUP_AT_MESSAGE_CREATE", "type=C2C_MESSAGE_CREATE"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("告警缺少 %s：\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "type=READY") {
		t.Errorf("READY 不应出现在缺 id 告警里：\n%s", logs.String())
	}
}

// TestIsMessageTypesCoverWarnSet 兜底：告警依赖的谓词一旦被改动（比如新增消息事件类型），
// 这里会同步暴露，避免「新类型缺 id 静默不报」。
func TestIsMessageTypesCoverWarnSet(t *testing.T) {
	types := []event.Type{event.C2CMessageCreate, event.GroupAtMessageCreate, event.GroupMessageCreate}
	for _, typ := range types {
		if !event.IsMessageType(typ) {
			t.Errorf("IsMessageType(%s) = false，该类型缺外层 id 将不再告警", typ)
		}
	}
	for _, typ := range []event.Type{event.InteractionCreate, event.GroupMemberAdd, "READY"} {
		if event.IsMessageType(typ) {
			t.Errorf("IsMessageType(%s) = true，控制类事件会被误报缺 id", typ)
		}
	}
}
