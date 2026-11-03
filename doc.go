// Package qqbot 是 QQ 官方机器人接入层 SDK：零业务依赖、可整体拆卸替换。
//
// 本仓库是独立仓库（github.com/kissqianfu/QQ-SDK）：主项目 aichat 通过
// `require github.com/kissqianfu/QQ-SDK` + 本地 `replace` 使用。
//
// 只做协议正确性与连接管理，不含任何业务概念（人设、记忆、权限都不在这里）。
// 依赖纪律：除标准库外，只有 WebSocket 阶段会引入 gorilla/websocket；其余一律不引。
//
// # 模块地图（包依赖只允许向下，禁止横向回环）
//
//	qqbot（门面）     Config / Mode（内置 ws|webhook 装配，见 ingress.go）/ Run / Events / Close
//	├─ transport      接入通道契约（Transport 接口）
//	│  ├─ transport/ws       WebSocket 接入（心跳/重连/Resume）
//	│  └─ transport/webhook  Webhook 接入（Ed25519 验签/队列派发）
//	├─ api            REST 客户端（发消息/撤回/富媒体/素材/流式/面板/菜单/群管理/互动）
//	│  └─ 依赖 apierr/token/event/send/qqtypes
//	├─ event          入站事件模型与解析（含 fuzz 覆盖）
//	├─ send           出站消息构造（文本/Markdown/键盘/卡片）
//	└─ 基础层（零依赖叶子）：
//	   ├─ apierr   平台错误码体系（IsRateLimited/IsExpired）
//	   ├─ token    access_token 缓存与刷新契约
//	   └─ qqtypes  平台 DTO 与跨包公共解析（如 ParseTime）
//
// 新增功能时先判断归属：协议解析进 event，出站构造进 send，REST 接口进 api
// （一个域一个文件）；跨包公共逻辑进 qqtypes；不新增横向依赖。
package qqbot
