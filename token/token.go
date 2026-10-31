// Package token 提供 access_token 的存取契约与默认内存实现。
//
// 刷新逻辑（提前 90s、失败重试、串行）在 api 层内部；业务只负责存取。
package token

import (
	"context"
	"sync"
	"time"
)

// Store 是 token 存取契约。单机器人单进程，内存实现就是最终实现。
type Store interface {
	Get(ctx context.Context) (string, bool, error)
	Set(ctx context.Context, token string, ttl time.Duration) error
}

// Memory 是线程安全的内存 TokenStore。
type Memory struct {
	mu     sync.RWMutex
	token  string
	expire time.Time
	now    func() time.Time
}

// NewMemory 创建内存 TokenStore。
func NewMemory() *Memory { return &Memory{now: time.Now} }

// Get 返回未过期的 token；不存在或已过期时 ok=false。
func (m *Memory) Get(ctx context.Context) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.token == "" || !m.now().Before(m.expire) {
		return "", false, nil
	}
	return m.token, true, nil
}

// Set 写入 token 与有效期。
func (m *Memory) Set(ctx context.Context, tok string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = tok
	m.expire = m.now().Add(ttl)
	return nil
}
