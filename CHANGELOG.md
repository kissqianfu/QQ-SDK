# 更新日志

对接入方（aichat 等主项目）：每次升级先看「接入方影响」小节；格式参考 Keep a Changelog。

## 未发版（2026-11-03，文档）— Markdown 长度上限实测（Q3）

- **结论**：平台文档只给错误码 40054007「消息长度超限」、不给数值。真实机器人实测
  **10,000 字符 Markdown 主动消息**（ASCII 10KB / 中文 30KB）均返回 200 且客户端完整显示，
  未触发任何长度限制 ⇒ **不存在 ~4000 的硬上限**（此前"按 4000 估"作废）。
- **处置**：`MaxContentLen` 保持默认 0（不校验）；README 写明它的定位是**展示体验**而非平台约束，
  建议 3000~4000。**无代码变更**。

## v0.3.1（2026-11-03）— 评审清单落地（互动身份、重试可配、配额退款、seq 取号等）

### 修复

- **C2C 互动事件拿不到操作人**：`INTERACTION_CREATE` 没有 `author`，单聊的操作人只在
  `d.user_openid`，而 `Event.Sender` 的兜底链漏了它 → 单聊按钮回调 `Sender.ID` 恒为空。
  按 `Sender.ID` 做按钮白名单时单聊会"全拒或空串放行"。已补兜底链，并新增明确字段
  `Event.InteractionUserID`（`resolved.user_id → group_member_openid → user_openid`）。
- **配额令牌失败不退**：主动消息在 `acquire` 后失败（Token/网络/5xx/429/4xx）会白扣两层桶令牌，
  宿主的持久化队列改期重发会再扣一次。现在 `Send` 用 delivered 标记精确退款：
  只有"HTTP 2xx 之前"的失败退令牌；2xx 之后（含响应体解析失败）不退——消息平台已接受。
  日上限仍只统计成功发送（v0.3.0 行为不变）。

### 新增

- **`RetryConfig`**（`api.Config.Retry`，门面 `qqbot.Config.Retry` 同步暴露）：可配
  `MaxAttempts` / `Base` / `MaxRetryAfter`。`MaxAttempts<0` = 关闭 SDK 内重试（交给宿主队列），
  `=1` = 只试一次，`=0` = 默认 3 次。默认行为不变。
- **`Config.MaxContentLen`**（默认 0 = 不校验）：文本/Markdown 正文超限时**本地拒绝**，
  不发请求、不扣额度，错误信息带 `MaxContentLen` 字样。
- **`send.SeqAllocator` + `send.NewMemorySeq()`**：被动回复 `msg_seq` 取号接口与内存实现；
  `Message.ReplyToWithSeq` / `ReplyToEventWithSeq` 取号后自动设置锚点。
  ⚠️ 内存实现重启归零；跨重启单调请注入自己的 SeqAllocator（DB/Redis）。
- **`ws.NewMemorySession()`**：单进程 SessionStore，进程内重连可 Resume；重启仍走 Identify。
- **`QuotaConfig.KeyCap`**：单关系桶/日计数表键上限（默认 4096）。
- **`webhook.Handler.DroppedByType()`**：按事件类型统计丢弃数（区分按钮回调与普通事件）。

### 接入方影响

- 全部向后兼容：默认行为不变（重试、长度校验、seq 仍由宿主决定）。
- 若宿主已有持久化发送队列：建议 `Retry: &api.RetryConfig{MaxAttempts: -1}`，避免两层重试叠加。
- 若宿主自己实现了按钮权限白名单：升级后单聊场景 `ev.Sender.ID` 不再为空，请确认
  "空 id 放行"的兜底分支可以删掉（否则逻辑仍偏宽）。
- `send.NewMemorySeq` 只适合单进程/测试；生产跨重启场景需自行实现 `SeqAllocator`。

## 未发版（2026-11-03，文档）— ARK 发送实测结论

- **结论**：v2 群/单聊发送侧**不支持** ARK（结构化卡片）。实测：`msg_type=3 + ark` 返回 200 但客户端收到空白消息；`text/markdown` 混发 `ark` 时 `ark` 被静默忽略。与 `type/overview.html` 的「发 ❌ / 收 ✅」一致，`ark.html` 的发送示例为历史遗留。
- **处置**：SDK 不新增 `send.Ark`，只在 README 写明"只支持接收 `ev.Ark`，需要卡片用 `send.NewCard`（msg_type=8）"，避免后续照 ark 页重复踩坑。**无代码变更**。

## 未发版（2026-11-03）— 对照 api-v2 文档的审计修复（建议发 v0.3.0）

### 修复（P0）

- **错误体字段是 `err_code`，不是 `code`**：官方《API 调用指南》明确失败体为 `{"err_code":...,"message":...,"trace_id":...}` 且要求按 `err_code` 判断。`api/http.go` 原先只读 `code` → `apierr.APIError.Code` 恒为 0，`IsDuplicate/IsExpired/IsAuth/IsRateLimited` 在生产里全部静默失效。现 err_code 优先、code 兜底
- **Webhook op13 的 `plain_token` 强校验过窄**：官方回调验证示例的 token 是 20 位字母数字（`Arq0D5A61EgUu4OxUvOp`），原先要求"32 位 hex"会把平台自己的挑战全部 400 拒掉 → 回调地址永远验证不通过。现放宽为 8~64 位字母数字；签名预言机仍被堵住（JSON 事件体必然含 `{ } " :`，过不了该口径）
- **撤回消息（此前宣称有、实际无）**：新增 `api.Client.Recall`（`DELETE /v2/{groups|users}/{openid}/messages/{message_id}`，发送后 2 分钟内）
- **单聊 `event_id` 被动回复锚点**：`C2C_MSG_RECEIVE`、`FRIEND_ADD` 现在会填 `Event.EventID`（官方单聊 event_id 支持 INTERACTION_CREATE / C2C_MSG_RECEIVE / FRIEND_ADD）；此前 `ReplyToEvent(ev.EventID)` 拿到空串会静默退化成主动消息
- **非幂等 POST 的传输层错误不再自动重试**：连接重置/读响应失败时请求可能已到达平台，重发会造成重复消息；5xx 上一轮已修，本轮补网络错误路径
- **WS 心跳写失败不再静默**：单帧写加 10s 超时（`SetWriteDeadline`），写失败主动关闭连接以打断读循环（不再等最长 5 分钟的 `StaleTimeout`）；读帧加 8MB 上限（`SetReadLimit`）
- **webhook 丢事件可观测**：队列满时调用 `Options.OnDrop(meta)`（含 event_id/type/原始体），宿主可落死信/告警；日志同时带 `dropped_total`
- **apierr 判定边界补齐**：`IsAuth` 含 HTTP 401 与 1124x/1125x/1126x/1127x/1128x、304004/304036/40034105/40034127；`IsExpired` 补 40034024/40034025/40034027；新增 `apierr` 包内单测（覆盖率 0% → 86.7%）
- **本地校验补齐**：指令面板（scope/target_type/元素数/name/desc/remark/link）、富媒体（file_type 1~4、URL 必须 http(s)、prepare 的 md5/sha1/md5_10m 必填且为 hex）、键盘按钮（id 必填且唯一、action.type 0/1/2、data 必填、permission_type 0~3）
- **`Retry-After` 支持 HTTP-date 形态**（RFC 9110 两种都合法，原先只认整数秒）
- **`Run` 无 Transport 时关闭 Events**：早退路径不再让 `range Events()` 的消费者永久阻塞
- **`Event.MsgID` 只对消息类事件赋值**：互动/成员/申请等事件的 `d.id` 语义不同，混进 MsgID 会被误当被动回复锚点
- **CI 护栏**：新增 gofmt 检查、UTF-8/BOM 检查、`go vet`、`go test -race`、全包 build（此前只有 test+build）

### 新增

- **消息**：`InputNotify`（`msg_type=6` 输入中状态，仅单聊）；`send.Message.IsWakeup`（互动召回，与 msg_id/event_id 互斥）；`ForceVerifyImageResource`（markdown 图片转存强校验）；键盘 `Anchor`（指令按钮唤起选图器）/ `PermissionType`（1 管理员 / 3 身份组）/ `SpecifyRoleIDs`；`send.AtEveryone` 常量
- **群管理（2026-08/09 文档新增）**：`SetGroupMemberMute`（POST restrict_chat_setting，≤20 条、最长 30 天）、`ListGroupMembers` / `ListGroupMembersAll` / `GroupMemberInfo`、`BatchRemoveMembers`、`ListMemberBlacklist` / `UpdateMemberBlacklist`、`JoinRequestList` / `ApprovalJoinRequest`（均含本地校验与分页翻页）
- **素材管理（2026-10-21 新增）**：`MaterialPrepare` / `MaterialPut` / `MaterialComplete` / `UploadMaterial`（三连封装）/ `ListMaterials` / `ListMaterialsAll` / `DeleteMaterial`，产出 `qqbot-media://` 引用供 Markdown 使用（与 `/files` 富媒体是两条链路）
- **自定义菜单（2026-08-12 新增）**：`GetMenu` / `PutMenu` / `DeleteMenu`（仅 C2C，≤10 项、子菜单 ≤5 且不可嵌套，按官方"汉字算 2"计宽校验）
- **机器人**：`GenerateURLLink`（分享链接 + callback_data ≤32 字符）
- **事件字段**：`Event.Timestamp`（平台事件时间，明确命名）、`Event.JoinRequest`（入群申请全字段）、`Event.Interaction`（`data.resolved`：button_id/button_data/feedback_opt/authorize_data 等）、`Event.SubscribeResults`（订阅消息授权结果）、`FriendScene` / `SceneParam`（FRIEND_ADD）、`GroupMemberOpenID`；`GROUP_MEMBER_ADD/REMOVE` 的 `member_openid` 与 `GROUP_MSG_REJECT` 的 `op_member_openid` 现在能落到 `Event.Sender`
- **ws**：`IntentGroupMemberEvent(1<<24)` / `IntentGuilds` 等 intent 常量、`DefaultIntents`、`ErrTerminalClose` 哨兵错误
- **api**：`Config.RetryNonIdempotent`；`token.Deleter` 可选接口（支持主动失效的存储实现它）
- **webhook**：`Options.QueueSize` / `Options.Workers` 可调（默认 256 / 32）

### 变更（行为，向后兼容但需知晓）

- **非幂等 POST 遇 5xx 默认不再自动重发**：平台可能已处理成功，重发会造成重复消息/重复资源。429 与网络错误仍重试；旧行为可显式 `api.Config{RetryNonIdempotent: true}`
- **401 自动换 token 重试一次**（当 token 存储实现 `token.Deleter`，默认 `token.Memory` 已实现）：平台提前吊销 token 时不再一直失败到 TTL
- **WS 关闭码三分类**：`4001/4002/4010~4014/4914/4915` → `Run` 返回 `ErrTerminalClose`（不再无限重连）；`4006/4007/4900~4913` → 先清 session 再 Identify；其余（4008/4009/1000/1001/传输错误）照旧退避重连
- **access_token 端点默认值**改为官方统一域名 `https://api.bot.qq.com/app/getAppAccessToken`（旧 `bots.qq.com` 仍可 `Config.TokenURL` 覆盖）
- **`message_reference` 只发官方字段 `message_id`**（去掉文档未定义的 `ignore_get_error`）
- **`Run` 返回后再调 `Run` 不再 panic**（历史实现会 `send on closed channel`），现在入口直接返回 nil
- **门面 `qqbot.Config` 新增 `Quota` / `RetryNonIdempotent`**：以前只有裸用 `api.New` 才能配，门面反而不可配

### 第二轮（残余 P2 收口 + 默认 intents 变更）

- **默认 intents 含群成员事件**：`ws.DefaultIntents` 改为 `1<<24 | 1<<25 | 1<<26`（群成员/入群申请 + 群/单聊消息 + 互动）。
  未获该能力的机器人订阅 1<<24 会被 4014 断开 → `Run` 返回 `ErrTerminalClose`；需要回退时显式传 `IntentGroupAndC2CEvent|IntentInteraction`。
- **webhook 生命周期**：新增 `Handler.Close()`（停止接收→503 让平台重推、关队列、等待已入队事件处理完，worker 回收、幂等）；
  新增 `Options.OnEventTimeout`（单条处理超时 ctx）；`Options.OnDrop` 覆盖"队列满/已关闭"两种丢弃。
- **响应体超限显式报错**：4MB 上限不再静默截断，返回"响应体超过上限"错误且不重试。
- **配额日上限只计成功发送**：`acquire` 只查不计数，发送成功后才 `noteSent`；平台 4xx/5xx/ctx 取消不再白占当日额度。
- **退避加抖动**：自建指数退避加 0~20% 抖动（`Retry-After` 精确值不加），避免多实例同步重试尖峰。
- **`GET /gateway/bot` 支持**：新增 `ws.Options.GatewayBot`（走 `GatewayEndpoint+"/bot"`，按平台建议分片打 Warn、打印 session 限额）
  与 `Transport.GatewayBot(ctx)`（宿主可自读 shards / session_start_limit）。
- **官方示例回归**：用官方文档给出的 appid/secret/plain_token/签名做端到端断言，锁死 seed repeat 与签名内容顺序。

### 废弃

- `send.NewMarkdownTemplate`：官方已把 `custom_template_id` / `template_id` 标为【已废弃】，新代码请用 `send.Markdown`
- `event.PartFace` / `PartMFace` / `PartAt`、`Part.FaceID` / `Part.Duration`：2026-11 文档无定义、解析从不产出，保留仅为不破坏既有 switch

### 接入方影响

- **编译兼容**：Bot（`go build ./...`）与 aichat 均已用本地 `replace` 编译通过；无导出符号删除，新增均为可选能力
- **Bot 升级需同步改一处测试**：`internal/qqaccess/send_fallback_test.go::TestNoFallbackOn401` 断言"401 只发 1 次"；SDK 现在会换 token 后重试一次（2 次）。生产语义是修复（否则要等 token TTL 才恢复），不是回归
- **WS 群成员/入群申请事件**：默认 intents 未包含 `1<<24`（未获平台权限的机器人订阅会 4014 断连）。需要时显式 `Intents: ws.DefaultIntents | ws.IntentGroupMemberEvent`
- **依赖 POST 5xx 重试**的宿主：显式 `RetryNonIdempotent: true`
- **频道（guild）场景仍不支持**：本 SDK 定位是 QQ 单聊 + 群聊，`/guilds/**`、频道 intents、频道私信均不在范围内（文档未删除，属有意的范围边界）

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
