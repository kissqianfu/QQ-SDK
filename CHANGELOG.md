# 更新日志

对接入方（aichat 等主项目）：每次升级先看「接入方影响」小节；格式参考 Keep a Changelog。

## 未发版（2026-11-02）— 协议兼容修复

### 修复

- **Webhook seed**：`webhook.New` 对 Bot Secret 由 `copy` 零填充改为按官方协议 repeat 到 32 字节；修复短 secret 下 `op=13` 挑战/事件验签失败
- **富媒体**：`msg_type=7` 发送体自动补 `content:" "`（官方要求该字段有值）
- **图文卡片**：`tuwen.content` 字段由 `desc` 改为官方 `description`
- **主动消息 seq**：`send.Message` 新增 `Seq(n)` / `SeqNo()`；主动消息可携带递增 `msg_seq`，避免平台按 seq + 内容判重
- **默认域名**：`api.ProdBaseURL` / `ws.DefaultGatewayEndpoint` 改为官方统一域名 `https://api.bot.qq.com`（旧域名仍可通过配置覆盖）

### 接入方影响

- 向后兼容；Webhook 调用方无需再自行展开 secret
- 主动消息建议改为 `send.Markdown(md).Seq(nextSeq())`；被动回复仍用 `ReplyTo`/`ReplyToEvent`
- 卡片/媒体无需改调用方式，SDK 发送体已修正
- 依赖旧默认域名的接入方如遇问题，显式设置 `BaseURL` / `GatewayEndpoint` 即可

## 未发版（HEAD `a545b09`，2026-10-31）— 建议发 v0.2.0

### 新增

- **单聊流式回复**（`api/stream.go`）：`StartStream` / `StreamReply.AppendDelta`（增量）/ `ReplaceFull`（全量纠错，本地前缀预校验）/ `Finish`；仅单聊，群 Target 直接报错；apierr 补流式频控码 50002
- **群管理查询**（`api/group.go`）：`GroupInfo`（群基本信息）/ `GroupBotState`（机器人群内状态）/ `GroupRestrictSetting`（禁言状态），均 30 QPM；前两者仅白名单机器人
- **指令面板 CRUD**（`api/panel.go`）：`CreatePanel` / `ListPanels` / `ListPanelsAll`（自动翻页）/ `PanelDetail` / `UpdatePanel` / `DeletePanel` / `UpdatePanelTargets`，scope/target/op 常量与本地校验
- **消息事件结构化字段**（`event`）：`Event.Mentions`（@ 列表，不含机器人自身）/ `Event.Ark`（ark_data 卡片）/ `Event.Elements`（msg_elements 递归引用元素，20 层深度护栏）
- **公共时间解析**（`qqtypes.ParseTime`）：RFC3339 字符串 / Unix 秒 / 毫秒数字三形态归一化，event 与 api 共用一份
- **fuzz 测试**：`event.Parse` 与 refIdxs（累计 2600 万+ 次执行零违反）；事件解析健壮性有回归语料兜底

### 修复

- 并发：`Close`/`Run` 双重 close 竞态（sync.Once）；webhook panic recover + 256 队列/32 worker 派发
- 协议：refIdxs 回退链断裂（未知 key 跳过裸字符串兜底）；`Envelope` 导出；FRIEND_ADD 取 d.openid；GROUP_ADD_ROBOT 取 op_member_openid；事件外层 id 作 event_id 锚点；附件 width/height/voice_wav_url/asr_refer_text；INTERACTION_CREATE 剥历史前缀
- 稳健性：ws StaleTimeout 判死与退避重置；REST 重试 ctx 感知；429 Retry-After；token 业务错误透传

### 变更（内部，零 API 破坏）

- 时间戳解析收敛到 `qqtypes.ParseTime`（删 event/api 双实现）
- api 包文件边界成文：HTTP 传输细节拆 `api/http.go`；`doc.go` 写入模块地图与新增功能归属纪律
- 死代码清除、重复函数收敛（runWithRetry / scenePath / getJSON+decodeJSON）

### 接入方影响

- **完全向后兼容**：不升级也能编译运行；`go get -u github.com/kissqianfu/QQ-SDK` 后零改动可用
- 新能力均为可选接入，建议按需启用（用法见 README 对应章节）：
  - 单聊回复想要打字机效果 → `api/stream.go`
  - 需要群画像 / 主动消息前置检查 → `GroupBotState.AllowProactiveMsg`
  - 需要识别用户分享的小程序/位置卡片 → `ev.Ark`
  - 需要理解"引用消息提问" → `ev.Elements` + `ev.RefMsgIdx`
  - 语音链路省转码/ASR → `Part.WavURL` / `Part.ASRText`
- 主项目 aichat 的分优先级适配清单（含 P1/P2/P3 与文件级落点）：工作区文档 `D:\AI Work\aichat-qqsdk-sync-2026-10-31.md`（不进仓库，含业务判断，仅本机）

## v0.1.0（`d2cb80e`，2026-10-31）

首个稳定版：门面（Config/Run/Events/Close）+ REST 客户端（token 自动刷新、发消息、撤回、Me、富媒体 URL 上传）+ 事件解析（消息/好友/群成员/互动/订阅回执 10+ 类）+ WebSocket 接入（心跳/重连/Resume/分片）+ Webhook 接入（Ed25519 验签/去重钩子）。依赖纪律：除 gorilla/websocket 外零第三方依赖。
