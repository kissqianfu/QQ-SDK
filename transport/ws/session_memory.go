package ws

// session_memory.go 提供单进程 SessionStore 实现。
//
// 为什么需要：Options.SessionStore 为 nil 时 SDK 从不 Resume——每次断线都 Identify，
// 而 Identify 之后平台**不会补发**断线窗口内的事件（只有 Resume 会按 seq 补）。
// 单实例部署注入了它就能覆盖"进程内重连"；跨重启仍会退化为 Identify，
// 这是内存实现的边界，必须写清楚，别让人误以为能当生产持久化用。

import (
	"context"
	"sync"
)

// MemorySession 是单进程内存 SessionStore：进程内重连可 Resume，**进程重启后走 Identify**。
//
// 多实例、或要求跨重启 Resume 的场景，请实现自己的 SessionStore（DB/Redis）并注入。
type MemorySession struct {
	mu   sync.Mutex
	sess Session
	ok   bool
}

// NewMemorySession 创建内存 SessionStore。
func NewMemorySession() *MemorySession { return &MemorySession{} }

// Load 实现 SessionStore。
func (m *MemorySession) Load(ctx context.Context) (Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sess, m.ok, nil
}

// Save 实现 SessionStore。
func (m *MemorySession) Save(ctx context.Context, s Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sess, m.ok = s, s.SessionID != ""
	return nil
}

// Clear 实现 SessionStore（平台要求重新 Identify 时调用）。
func (m *MemorySession) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sess, m.ok = Session{}, false
	return nil
}
