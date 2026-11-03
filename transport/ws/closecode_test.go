package ws

// closecode_test.go 锁定关闭码三分类（依据官方 WebSocket 错误码表）：
// terminal=停止重连；resumeForbidden=清 session 后 Identify；reconnect=退避重连。

import (
	"errors"
	"testing"

	"github.com/gorilla/websocket"
)

func TestClassifyClose(t *testing.T) {
	cases := []struct {
		code int
		want closeAction
	}{
		{4001, closeTerminal},        // 无效 opcode
		{4002, closeTerminal},        // 无效 payload
		{4010, closeTerminal},        // 无效 shard
		{4011, closeTerminal},        // 需要分片
		{4012, closeTerminal},        // 无效 version
		{4013, closeTerminal},        // 无效 intent
		{4014, closeTerminal},        // intent 无权限
		{4914, closeTerminal},        // 机器人已下架
		{4915, closeTerminal},        // 机器人已封禁
		{4006, closeResumeForbidden}, // 无效 session id
		{4007, closeResumeForbidden}, // seq 错误
		{4900, closeResumeForbidden}, // 内部错误
		{4913, closeResumeForbidden},
		{4008, closeReconnect}, // 发送过快
		{4009, closeReconnect}, // 连接过期
		{1000, closeReconnect}, // 正常关闭
		{1001, closeReconnect}, // going away
		{1006, closeReconnect}, // 异常关闭（传输层）
		{0, closeReconnect},    // 未知
	}
	for _, tc := range cases {
		if got := classifyClose(tc.code); got != tc.want {
			t.Errorf("classifyClose(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestDefaultIntentsIncludesGroupMember(t *testing.T) {
	// 2026-11-03 用户拍板：默认包含 1<<24（群成员/入群申请事件）。
	// 若机器人无该权限会被 4014 断开，此时应显式调小 Intents（见 ws.go 注释）。
	if DefaultIntents&IntentGroupMemberEvent == 0 {
		t.Fatal("DefaultIntents 应包含 IntentGroupMemberEvent")
	}
	if DefaultIntents != IntentGroupMemberEvent|IntentGroupAndC2CEvent|IntentInteraction {
		t.Fatalf("DefaultIntents = %#x", DefaultIntents)
	}
	if IntentGroupMemberEvent != 1<<24 || IntentGroupAndC2CEvent != 1<<25 || IntentInteraction != 1<<26 {
		t.Fatalf("intent 位错误: %#x %#x %#x", IntentGroupMemberEvent, IntentGroupAndC2CEvent, IntentInteraction)
	}
}

func TestReadErrorWrapsCloseCode(t *testing.T) {
	err := readError(&websocket.CloseError{Code: 4014, Text: "intent not allowed"})
	var ce *wsCloseError
	if !errors.As(err, &ce) {
		t.Fatalf("readError() = %T %v, want *wsCloseError", err, err)
	}
	if ce.code != 4014 || ce.text != "intent not allowed" {
		t.Fatalf("close error = %+v", ce)
	}
}
