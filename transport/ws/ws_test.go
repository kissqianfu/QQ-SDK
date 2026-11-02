package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kissqianfu/QQ-SDK/event"
)

// memStore 是内存 SessionStore（测试用）。
type memStore struct {
	mu      sync.Mutex
	session Session
	saved   int
	cleared int
}

func (m *memStore) Load(context.Context) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session, m.session.SessionID != "", nil
}

func (m *memStore) Save(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session = s
	m.saved++
	return nil
}

func (m *memStore) Clear(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session = Session{}
	m.cleared++
	return nil
}

// fakeGateway 起一个假网关：hello → 读 Identify/Resume → 发事件 → 收心跳。
type fakeGateway struct {
	t      *testing.T
	srv    *httptest.Server
	first  chan map[string]any
	hb     chan map[string]any
	frames chan map[string]any // 服务端主动下发的帧
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{
		t:      t,
		first:  make(chan map[string]any, 1),
		hb:     make(chan map[string]any, 8),
		frames: make(chan map[string]any, 4),
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 40}})
		var first map[string]any
		if err := conn.ReadJSON(&first); err != nil {
			return
		}
		g.first <- first
		for _, frame := range g.drainFrames() {
			_ = conn.WriteJSON(frame)
		}
		// 之后持续收心跳（同时允许主动下发新事件）。
		readErr := make(chan error, 1)
		go func() {
			for {
				var m map[string]any
				if err := conn.ReadJSON(&m); err != nil {
					readErr <- err
					return
				}
				g.hb <- m
			}
		}()
		for {
			select {
			case frame := <-g.frames:
				_ = conn.WriteJSON(frame)
			case <-readErr:
				return
			case <-time.After(5 * time.Second):
				return
			}
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) drainFrames() []map[string]any {
	var out []map[string]any
	for {
		select {
		case f := <-g.frames:
			out = append(out, f)
		default:
			return out
		}
	}
}

func (g *fakeGateway) wsURL() string {
	return "ws" + strings.TrimPrefix(g.srv.URL, "http")
}

func (g *fakeGateway) push(frame map[string]any) { g.frames <- frame }

func dispatchFrame(seq int64, typ, id string, d map[string]any) map[string]any {
	return map[string]any{"op": 0, "s": seq, "t": typ, "id": id, "d": d}
}

// runAndCancel 在后台跑 transport，返回事件通道与取消函数。
func runAndCancel(t *testing.T, tr *Transport) (<-chan *event.Event, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *event.Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- tr.Run(ctx, func(ev *event.Event) error {
			events <- ev
			return nil
		})
	}()
	t.Cleanup(cancel)
	return events, cancel, done
}

func TestTransportIdentifyHeartbeatDispatch(t *testing.T) {
	g := newFakeGateway(t)
	store := &memStore{}
	tr, err := New(Options{
		AppID:             "app-1",
		Token:             func(context.Context) (string, error) { return "tok", nil },
		SessionStore:      store,
		GatewayURL:        g.wsURL(),
		Intents:           1 << 25,
		HeartbeatInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	events, cancel, done := runAndCancel(t, tr)

	first := <-g.first
	if first["op"].(float64) != 2 {
		t.Fatalf("first frame op = %v, want 2 (Identify)", first["op"])
	}
	d := first["d"].(map[string]any)
	if d["token"] != "QQBot tok" {
		t.Fatalf("identify token = %v", d["token"])
	}
	shard := d["shard"].([]any)
	if shard[0].(float64) != 0 || shard[1].(float64) != 1 {
		t.Fatalf("shard = %v", shard)
	}

	g.push(dispatchFrame(1, "READY", "EV-0", map[string]any{"session_id": "sess-1"}))
	g.push(dispatchFrame(2, "C2C_MESSAGE_CREATE", "EV-1", map[string]any{
		"id": "msg-1", "content": "hi", "author": map[string]any{"user_openid": "u-1"},
	}))

	got := map[event.Type]*event.Event{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-events:
			got[ev.Type] = ev
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for events")
		}
	}
	if got[event.C2CMessageCreate] == nil || got[event.C2CMessageCreate].MsgID != "msg-1" {
		t.Fatalf("c2c event = %+v", got[event.C2CMessageCreate])
	}

	// 心跳必须带最近 seq。
	select {
	case hb := <-g.hb:
		if hb["op"].(float64) != 1 {
			t.Fatalf("heartbeat op = %v, want 1", hb["op"])
		}
		if hb["d"].(float64) != 2 {
			t.Fatalf("heartbeat seq = %v, want 2", hb["d"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat received")
	}

	store.mu.Lock()
	saved := store.session
	store.mu.Unlock()
	if saved.SessionID != "sess-1" || saved.Seq != 2 {
		t.Fatalf("saved session = %+v", saved)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestStaleConnectionReconnects(t *testing.T) {
	g := newFakeGateway(t)
	tr, err := New(Options{
		AppID:             "app-1",
		Token:             func(context.Context) (string, error) { return "tok", nil },
		GatewayURL:        g.wsURL(),
		Intents:           1 << 25,
		HeartbeatInterval: 30 * time.Millisecond,
		StaleTimeout:      150 * time.Millisecond, // 网关不发任何帧 → 读超时判死 → 重连
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	events, cancel, done := runAndCancel(t, tr)
	_ = events

	// 第一次连接 + 判死后重连：应看到两次 Identify（op=2）。
	for i := 1; i <= 2; i++ {
		select {
		case first := <-g.first:
			if first["op"].(float64) != 2 {
				t.Fatalf("connect #%d op = %v, want 2 (Identify)", i, first["op"])
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for redial #%d", i)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestTransportResume(t *testing.T) {
	g := newFakeGateway(t)
	store := &memStore{session: Session{SessionID: "sess-9", Seq: 41}}
	tr, err := New(Options{
		AppID:             "app-1",
		Token:             func(context.Context) (string, error) { return "tok", nil },
		SessionStore:      store,
		GatewayURL:        g.wsURL(),
		HeartbeatInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	events, cancel, done := runAndCancel(t, tr)

	first := <-g.first
	if first["op"].(float64) != 6 {
		t.Fatalf("first frame op = %v, want 6 (Resume)", first["op"])
	}
	d := first["d"].(map[string]any)
	if d["session_id"] != "sess-9" || d["seq"].(float64) != 41 {
		t.Fatalf("resume payload = %+v", d)
	}

	g.push(dispatchFrame(42, "GROUP_AT_MESSAGE_CREATE", "EV-9", map[string]any{
		"id": "msg-9", "content": "resume", "group_openid": "g-1",
		"author": map[string]any{"member_openid": "m-1"},
	}))
	select {
	case ev := <-events:
		if ev.Type != event.GroupAtMessageCreate {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for resumed event")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestDefaultGatewayEndpoint(t *testing.T) {
	if DefaultGatewayEndpoint != "https://api.bot.qq.com/gateway" {
		t.Fatalf("DefaultGatewayEndpoint = %q, want https://api.bot.qq.com/gateway", DefaultGatewayEndpoint)
	}
}
