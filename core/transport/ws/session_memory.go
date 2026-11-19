package ws

// session_memory.go 提供单进程 SessionStore：进程内重连可 Resume，跨重启走 Identify。

import (
	"context"
	"sync"
)

// MemorySession 是单进程内存 SessionStore；跨重启 Resume 请自实现 SessionStore 注入。
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
