// Package transport 定义接入通道契约（WS / Webhook / 自定义）。
package transport

import (
	"context"
	"errors"

	"github.com/kissqianfu/QQ-SDK/event"
)

// ErrClosed 表示接入通道已关闭：emit 在收尾阶段返回它，transport 应停止而不是继续投递。
var ErrClosed = errors.New("transport: 通道已关闭")

// Shard 是分片信息；单机器人固定 {0,1}，保留字段仅为协议完整。
type Shard struct {
	Index int
	Count int
}

// Transport 是一条接入通道。SDK 不替业务起 server，也不抢端口。
type Transport interface {
	// Run 阻塞运行；收到事件调 emit；返回 nil 表示 ctx 取消。
	Run(ctx context.Context, emit func(*event.Event) error) error
	// Name 返回通道名（"websocket" | "webhook"）。
	Name() string
}
