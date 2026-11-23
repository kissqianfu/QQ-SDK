// Package qqbot 是 QQ 官方机器人接入层 SDK 门面：装配 Config → ws/webhook/api 的
// 启动与收尾，不隐藏 core 类型。依赖纪律：除标准库外仅 WebSocket 用 gorilla/websocket。
//
// # 模块地图（包依赖只允许向下，禁止横向回环）
//
//	qqbot（门面）  Config / Run / Events / Close
//	└─ core/       api（REST）/ event（事件解析）/ send（出站构造）/ transport（ws|webhook）
//	              / apierr（错误码）/ token（凭证）/ qqtypes（公共 DTO）/ version（版本与 UA）
//
// 叶子包（apierr、token、qqtypes、version）不依赖任何同模块包，其余层只允许向下引用。
package qqbot
