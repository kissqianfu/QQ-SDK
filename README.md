# QQ-SDK

QQ 官方机器人接入层 SDK（Go）：零业务依赖、可整体拆卸替换。

- 接入：WebSocket + Webhook 双通道（`Transport` 接口，SDK 不替业务起 server）
- REST：access_token 刷新（提前 90s、互斥、401 自动换票重试一次）、群/单聊发消息、撤回、`Me`（`GET /users/@me`）、富媒体上传、**素材管理**（`qqbot-media://`）、互动事件响应（`PUT /interactions/{id}`）、**分享链接**
- 消息：文本 / 原生 Markdown / **Markdown 模板**（`custom_template_id`+params，官方已标废弃）/ 富媒体 / 图文卡片 / **输入中状态（msg_type=6）** / **互动召回（is_wakeup）**；键盘支持自定义与**模板短形式**（`{"keyboard":{"id":...}}`）；被动回复锚点支持 `msg_id` 与 `event_id` 二选一
- 协议正确性内置：`send.Message` 构造器做本地校验（卡片 title≤50 / url 必须 https / 键盘行列上限等）
- 事件：平台语义的 `event.Event`（无业务字段）；含 Mentions / Ark / Elements / **JoinRequest / Interaction(data.resolved) / SubscribeResults**
- 错误：`apierr.APIError` 判定方法（`IsDuplicate` / `IsExpired` / `IsAuth` / `IsRateLimited`），按官方 `err_code` 解析
- 稳健性内置：WS 心跳判活（读空闲超时自动重连）、指数退避（稳定连接断线后重置）、**关闭码三分类**（下架/封禁/intent 无权限 → 停止重连）、429 `Retry-After`、ctx 取消即停重试、非幂等 POST 5xx 不自动重发
- 发送前**主动配额**内置（三层：群聊 60 QPM / 单聊 10 QPS / 单关系 20 QPM + 每目标每日 1000 条），本地拒绝时不触达平台
- 群管理：群信息 / 机器人群内状态 / 禁言查询与**设置** / **群成员列表与详情** / **批量移出** / **群黑名单** / **入群申请列表与审批**
- 指令面板（`/v2/panels`）+ **全局自定义菜单（`/v2/menu`）**
- **范围边界**：只做 QQ 单聊 + 群聊。频道（guild）域的 `/guilds/**`、频道 intents、频道私信均**不支持**

## 安装

```bash
go get github.com/kissqianfu/QQ-SDK
```

## WebSocket 接入（推荐）

```go
import (
    qqbot "github.com/kissqianfu/QQ-SDK"
    "github.com/kissqianfu/QQ-SDK/api"
    "github.com/kissqianfu/QQ-SDK/event"
    "github.com/kissqianfu/QQ-SDK/send"
    "github.com/kissqianfu/QQ-SDK/transport/ws"
)

c, err := qqbot.New(qqbot.Config{
    AppID: "app", AppSecret: "secret",
    Transport: mustWS(c), // 见下
    // 可选：三层配额（门面也能配置/关闭；nil = 默认口径）
    // Quota: &api.QuotaConfig{GroupQPM: 60, SingleQPS: 10},
})
if err != nil { return err }
go func() {
    for ev := range c.Events() {
        if ev.Type == event.GroupAtMessageCreate {
            _, _ = c.Sender().Send(ctx, api.Target{Scene: ev.Scene, ID: ev.TargetID},
                send.Text("hi").ReplyTo(ev.MsgID, 1))
        }
    }
}()
// Run 阻塞运行；内部管理心跳、断线重连、Resume。
err = c.Run(context.Background())
```

WS transport 需要把 REST 客户端的 token 提供给网关：

```go
func mustWS(c *qqbot.Client) transport.Transport {
    tr, err := ws.New(ws.Options{
        AppID: appID,
        Token: c.Sender().Token, // 复用 REST 的 token 缓存
        // SessionStore: 持久化 session/seq 用于 Resume。
        // 单实例可用 ws.NewMemorySession()（进程内重连可 Resume，重启走 Identify）；
        // 多实例/要求跨重启 Resume 请自己实现（DB/Redis）。
        SessionStore: ws.NewMemorySession(),
        // Dedupe: 自选，事件幂等钩子
    })
    if err != nil { panic(err) }
    return tr
}
```

## Webhook 接入

SDK 只提供 `http.Handler`，端口/TLS/反代由业务决定：

```go
h, _ := webhook.New(webhook.Options{
    Secret: appSecret,       // AppSecret（SDK 内部按官方协议 repeat 到 32 字节）
    AppID:  appID,           // 可选：校验 X-Bot-Appid
    OnEvent: func(ctx context.Context, ev *event.Event) { /* 异步处理 */ },
    // Deduper: 自选，重复事件只 ACK 不回调
})
mux.Handle("/callback", h)
```

平台 3s 超时会重推：Handler 先 ACK 再异步回调；op13 URL 校验挑战按官方协议内置。
`Handler.Close()` 会停止接收（之后新请求返回 503，平台会重推）、关闭队列并等待已入队事件处理完；
`Options.OnEventTimeout` 给单条处理加超时 ctx；队列满时调 `Options.OnDrop`（ACK 后弃事件不可恢复）；
`Handler.DroppedByType()` 按事件类型统计丢弃数（按钮回调和普通事件要分开看）。

## 按钮互动（INTERACTION_CREATE）

用户点按钮后**必须回应**，否则客户端一直 loading 直到超时：

```go
for ev := range c.Events() {
    if ev.Type == event.InteractionCreate && (ev.InteractionType == 11 || ev.InteractionType == 12) {
        // ev.InteractionType: 11=消息按钮 12=快捷菜单（13/14/... 无需回应）
        _ = c.Sender().Interact(ctx, ev.EventID, api.InteractOK)
        // 业务处理后用 event_id 锚点回复：
        _, _ = c.Sender().Send(ctx, api.Target{Scene: ev.Scene, ID: ev.TargetID},
            send.Text("收到").ReplyToEvent(ev.ID, 1))
    }
}
```

## 发送消息

```go
target := api.Target{Scene: event.SceneGroup, ID: groupOpenID}

// 主动 Markdown；主动消息建议设置递增 msg_seq（平台按 seq + 内容去重）
_, _ = c.Sender().Send(ctx, target, send.Markdown(md).Seq(nextSeq()))

// 被动回复：msg_id 锚点
_, _ = c.Sender().Send(ctx, target, send.Markdown(reply).ReplyTo(ev.MsgID, 1))

// 被动回复：event_id 锚点
_, _ = c.Sender().Send(ctx, target, send.Markdown(reply).ReplyToEvent(ev.ID, 1))
```

### 撤回 / 输入中 / 互动召回 / @全体

```go
res, _ := c.Sender().Send(ctx, target, send.Text("临时消息"))
_ = c.Sender().Recall(ctx, target, res.ID)   // 2 分钟内；管理员可撤群成员消息

// 单聊"正在输入"（msg_type=6），需被动回复锚点；最长 60 秒
_ = c.Sender().InputNotify(ctx, userOpenID, ev.MsgID, 1, 30)

// 互动召回：用户主动对话后 30 天内的 4 个周期各可下发 1 条（与 msg_id/event_id 互斥）
_, _ = c.Sender().Send(ctx, api.Target{Scene: event.SceneC2C, ID: userOpenID},
    send.Text("好久不见，要试试新功能吗").IsWakeup(true))

// 群 @全体成员（需群管理员；每群每天 1 次，超限平台返回 304019）
_, _ = c.Sender().Send(ctx, target, send.Markdown("**维护通知** " + send.AtEveryone))
```

Markdown 还可以打开图片转存强校验（转存失败就报错而不是静默丢图）：
`send.Markdown(md).ForceVerifyImageResource(true)`。

### 键盘按钮

`send.Button` 支持官方 `action` 的完整字段：`Anchor=1`（指令按钮点击后唤起手Q选图器）、
`PermissionType`（1=管理员 / 3=指定身份组，零值自动：有指定对象→0，否则 2=所有人）、
`SpecifyUserIDs` / `SpecifyRoleIDs`、`Modal`（二次确认）、`Enter` / `Reply`（指令按钮）。

### 被动回复 msg_seq 取号（v0.3.1）

平台按「锚点 + `msg_seq`」判重，重复会返回 40054005，而它会被 `IsDuplicate()` 当成功 —
**用户什么都没收到，调用方以为成功**。所以同一 `msg_id` 多次回复必须递增 seq：

```go
seqs := send.NewMemorySeq()                    // 进程内实现；跨重启单调请自实现 SeqAllocator

msg, err := send.Text("收到").ReplyToWithSeq(ctx, seqs, ev.MsgID)          // msg_id 锚点
msg, err  = send.Text("收到").ReplyToEventWithSeq(ctx, seqs, ev.EventID)   // event_id 锚点
```

两类锚点分别计数（不能共用）；`NewMemorySeq` 重启归零，生产跨重启场景请注入 DB/Redis 实现。

## 出站主动消息配额（三层）

`Client.Send` 是所有发送形态的唯一出口，配额就实现在这里，**宿主不需要也不应该再自己计数**：

| 层级 | 维度 | 默认值 |
|---|---|---|
| ① 全局 · 群聊 | 全部群消息共用一条桶 | 60 QPM |
| ① 全局 · 单聊 | 全部单聊消息共用一条桶（与群聊互相独立） | 10 QPS |
| ② 单关系 | 按 `group_openid` / `user_openid` | 20 QPM |
| ③ 日上限 | 每目标每天（日界 UTC+8 00:00，只查不消耗） | 1000 条 |

> **升级注意（v0.1.0 → v0.2.0）**：配额是 v0.2.0 新增的，且**默认开启**——升级后宿主的
> `api.Config{}` 一行不改，主动消息也会开始被本地限流。对大多数宿主这是"白得一层保护"，
> 但两种情况请先自查：① 已有自己的发送队列/限流，想保留单一权威口径 → 显式 `Disable`；
> ② 存在批量主动推送（整点提醒、群发）→ 按实际峰值调 `GroupQPM`/`SingleQPS`，否则超出部分会
> 收到 `ErrThrottledLocal`。另外错误处理里若有 `errors.As(err, &apierr.APIError)`，
> `ThrottleError` **不会**命中它（两者是不同错误类型），需要单独 `errors.Is/As` 判断，
> 否则日志里只留一行字符串、看不到 `layer`。

```go
// 默认即开启，按上表口径；要调就传 Quota（api.New 与门面 qqbot.New 都支持）：
c, _ := api.New(api.Config{
    AppID: appID, AppSecret: appSecret,
    Quota: &api.QuotaConfig{GroupQPM: 60, SingleQPS: 10, RelationQPM: 20, DailyPerTarget: 1000},
})
// 需要整体关闭时：Quota: &api.QuotaConfig{Disable: true}
```

> **未认证机器人**：平台对未认证 bot 的额度更低（群 30 qpm；单聊 5 qps 且 30 qpm）。
> SDK 默认按"已认证"口径（60/10），未认证宿主请显式调小，否则本地放行但平台会 429。

> **失败退款（v0.3.1）**：主动消息在平台接受之前失败（Token/网络/5xx/429/4xx）会退回令牌，
> 宿主的持久化队列改期重发不会双重扣额；日上限本来就只统计成功发送。
> 键上限 `QuotaConfig.KeyCap` 可配（默认 4096），超出按 FIFO 淘汰最老目标。

- **被动回复不计入**：带 `msg_id`（`ReplyTo`）或 `event_id`（`ReplyToEvent`）的消息直接放行。
- **被本地拒绝时不会调用平台**：判定发生在取 token/发请求之前，这才真的省额度。
- **错误可区分**：`errors.Is(err, apierr.ErrThrottledLocal)` 为真；`errors.As(err, &te)` 可取
  `te.Layer`（`group`/`single`/`relation`/`daily`）与 `te.RetryAfter`。
  它和平台 429（`APIError.IsRateLimited()`）**不是一类**：本地拒绝重试无意义，应走兜底通道；
  平台 429 才值得等 `Retry-After`。
- **指标**：`c.QuotaStats()` 返回三层各自的拒绝次数，宿主可导出
  `xxx_throttled_total{layer}`。
- ⚠️ **best-effort**：计数在内存里，重启即重置，多实例之间不共享。它是告警风暴时的自保，
  **不是权威额度**——权威口径永远是平台返回的 429。

## 富媒体上传

```go
// 远程 URL：平台自己拉取
fileInfo, _ := c.Sender().UploadFile(ctx, event.SceneGroup, groupOpenID, imageURL, 1)

// 本地小文件（≤5M）：base64 直传，一次调用搞定
fileInfo, _ := c.Sender().UploadFileData(ctx, event.SceneGroup, groupOpenID, "sticker.png", 1, data)

// 本地大文件：走官方分片链路（prepare → 分片 PUT → part_finish → 合并）
fileInfo, _ := c.Sender().UploadFileBytes(ctx, event.SceneGroup, groupOpenID, "voice.mp3", 3, data)

// 需要完整响应（file_uuid / ttl 有效期 / raw_url）时用对应 Detail 方法：
res, _ := c.Sender().UploadFileDetail(ctx, event.SceneGroup, groupOpenID, imageURL, 1)
// res.TTL 是 file_info 有效期（秒），0 表示长期有效；过期需重新上传

_, _ = c.Sender().Send(ctx, api.Target{Scene: event.SceneGroup, ID: groupOpenID}, send.Media(fileInfo))
```

## ARK（结构化卡片）：v2 只支持接收，不支持发送

> **实测结论（2026-11-03，真实机器人 + 真实群）**：官方 `server-inter/message/type/ark.html`
> 给的发送结构在 v2 群/单聊接口上**不生效**：`msg_type=3 + ark` 返回 200 但客户端收到的是
> **空消息**；`text/markdown` 混发 `ark` 时 `ark` 被静默忽略、只渲染文本。
> 这与 `type/overview.html` 的「结构化卡片 发 ❌ / 收 ✅」一致——ark 页里的发送示例是历史遗留。
>
> 因此 SDK **不提供** `send.Ark`：只解析接收侧（`ev.Ark`，见「消息事件结构化字段」）。
> 需要卡片形态时请用**图文卡片 `send.NewCard`（msg_type=8）**。

## 单聊流式回复（打字机效果）

平台仅**单聊**支持流式（群聊无此接口，传群 Target 会直接报错）。原理：多次 `POST /stream_messages`，服务端按分片合并；`input_state` 1=生成中、10=结束。

```go
// 开流：首片立刻让用户看到"正在生成"
st, err := c.Sender().StartStream(ctx, api.Target{Scene: event.SceneC2C, ID: userOpenID},
    "正在生成回答，请稍候", api.StreamConfig{MsgID: ev.MsgID, ContentType: api.StreamMarkdown})

// LLM delta 逐段追加（增量模式）
for delta := range llmCh {
    frame, err := st.AppendDelta(ctx, delta)
    // frame.RemainMsgLen 接近 0 时应尽快收尾或截断
}

// 或用全量模式纠错（须保持已下发前缀，本地预校验省一次 40007 往返）
st.ReplaceFull(ctx, fullText)

st.Finish(ctx) // input_state=10，结束
```

注意事项：同一时间只允许一个 goroutine 驱动一条流（平台按 index 串行合并）；锚点 `msg_id`/`event_id` 二选一；接口限频 50 QPS，触发频控时错误满足 `apierr.IsRateLimited()`（含流式专属码 50002）。

## 群管理查询

三个只读接口（各限 30 QPM；`GroupInfo` 与 `GroupBotState` 仅白名单机器人可用，`GroupRestrictSetting` 需机器人有群管理员身份）：

```go
info, _ := c.Sender().GroupInfo(ctx, groupOpenID)            // 群名/简介/分类/标签/成员数
state, _ := c.Sender().GroupBotState(ctx, groupOpenID)       // 入群时间/是否允许主动消息/接收设置/机器人角色
rs, _ := c.Sender().GroupRestrictSetting(ctx, groupOpenID)   // 全员禁言规则 + 禁言中的成员列表
```

## 群治理：禁言 / 成员 / 黑名单 / 入群申请

2026-08/09 文档新增的一批群管理能力（除禁言外均标注「内邀接入中」，未开通返回 11253）。所有方法都做本地校验（条数上限、op 取值、禁言 30 天上限）：

```go
s := c.Sender()

// 设置/更新/解除成员禁言（单次 ≤20；add/update 必须给到期时间，最长 30 天）
err = s.SetGroupMemberMute(ctx, groupOpenID, []api.MemberMuteOp{
    {Op: api.MuteOpAdd, MemberOpenID: m1, MuteExpireAt: "2026-11-10T12:00:00+08:00"},
    {Op: api.MuteOpDel, MemberOpenID: m2},
})

// 群成员（内邀）
members, _ := s.ListGroupMembers(ctx, groupOpenID, "")   // cursor 翻页
all, _ := s.ListGroupMembersAll(ctx, groupOpenID)        // 自动翻页（护栏 100 页）
one, _ := s.GroupMemberInfo(ctx, groupOpenID, memberOpenID)

// 批量移出（≤20）+ 可选拉黑
rm, _ := s.BatchRemoveMembers(ctx, groupOpenID, api.BatchRemoveMembersRequest{
    MemberOpenIDs: []string{m1, m2}, AddToMemberBlacklist: true,
})

// 群黑名单（内邀）
bl, _ := s.ListMemberBlacklist(ctx, groupOpenID, "", 50)
res, _ := s.UpdateMemberBlacklist(ctx, groupOpenID, api.MemberBlacklistRequest{
    Op: api.BlacklistOpAdd, MemberOpenIDs: []string{m3},
})

// 入群申请：列表 → 审批（需要 GROUP_JOIN_REQUEST 事件或列表里的 join_request_id）
reqs, _ := s.JoinRequestList(ctx, groupOpenID, "", 20)
err = s.ApprovalJoinRequest(ctx, groupOpenID, memberOpenID, api.JoinApprovalRequest{
    Op: api.JoinOpDecline, JoinRequestID: reqs.List[0].JoinRequestID,
    RejectReason: "满员", AddToMemberBlacklist: true,
})
```

`GROUP_JOIN_REQUEST` 事件经 SDK 归一化为 `ev.JoinRequest`（含 `JoinRequestID` / `ApplySource` / `VerifyInfo` / `AutoApprovedStrategyID`），可直接驱动审批：

```go
if ev.Type == event.GroupJoinRequest && ev.JoinRequest != nil {
    err := s.ApprovalJoinRequest(ctx, ev.TargetID, ev.JoinRequest.MemberOpenID, api.JoinApprovalRequest{
        Op: api.JoinOpApprove, JoinRequestID: ev.JoinRequest.JoinRequestID,
    })
}
```

## 指令面板

C 端输入框 "/" 拉起的指令面板（单聊/群聊/文字子频道/频道私信四场景），全量 CRUD：

```go
s := c.Sender()

// 创建（一个机器人最多 20 个；c2c/group 支持 specific 定向生效，channel/dm 只能全局）
id, _ := s.CreatePanel(ctx, api.PanelCreateRequest{
    Scope:        api.PanelScopeGroup,
    TargetType:   api.PanelTargetSpecific,
    GroupOpenIDs: []string{groupOpenID},
    Panel: api.Panel{Items: []api.PanelItem{
        {Type: api.PanelItemCommand, Name: "群签到", Desc: "每日签到"},
        {Type: api.PanelItemLink, Name: "更多服务", Link: "https://example.com"},
    }, Remark: "签到面板"},
})

list, _ := s.ListPanels(ctx, api.PanelScopeGroup, "", 20) // scope 必填；cursor/limit 分页
detail, _ := s.PanelDetail(ctx, id)                        // 含关联的 user/group openid 列表
ver, _ := s.UpdatePanel(ctx, id, api.Panel{Items: ...})    // 覆盖元素与备注，不影响关联对象

// 增删关联对象（每次最多 20 个；全局面板不支持）
s.UpdatePanelTargets(ctx, id, api.PanelTargetRequest{Op: api.PanelTargetOpAdd, GroupOpenIDs: []string{g2}})
s.DeletePanel(ctx, id)
```

错误码 40030006 面板不存在、40030011 scope 不合法、40030021 全局面板不支持关联对象，均通过 `apierr.APIError.Code` 判断。

## 全局自定义菜单

和指令面板不同：菜单是**单聊输入框旁**的固定入口，全局唯一、对所有用户一致（仅 C2C）。PUT 是全量覆盖，`DeleteMenu` 等于清空：

```go
state, _ := c.Sender().GetMenu(ctx)          // 含 Menu + Version（从未配置时 version=0）

ver, _ := c.Sender().PutMenu(ctx, api.Menu{Items: []api.MenuItem{
    {Name: "帮助", Type: api.MenuItemSendMessage, SendMessage: "/help"},
    {Name: "官网", Type: api.MenuItemLink, Link: "https://example.com"},
    {Name: "更多", Type: api.MenuItemMenu, SubMenuItems: []api.MenuSubItem{
        {Name: "设置", Type: api.MenuItemSendMessage, SendMessage: "/settings"},
    }},
    {Name: "联网", Type: api.MenuItemSwitch, Switch: &api.MenuSwitch{SwitchID: "web", Default: true}},
}})
```

本地校验按官方口径：菜单项 ≤10、子菜单 ≤5 且不能再嵌套；名称按「ASCII 算 1、汉字算 2」计宽（一级 ≤10 / 二级 ≤14）；link 必须 `https://`。

## 素材管理（Markdown 图片，qqbot-media://）

2026-10-21 新增：把图片预上传到平台换取 `qqbot-media://` 引用，**不依赖公网图床**。注意它与 `/files` 富媒体（msg_type=7）是两条链路：

```go
img, _ := c.Sender().UploadMaterial(ctx, "kline.png", "image/png", pngBytes)
// img.URL = qqbot-media://image/v1/<token>，可直接写进 Markdown：
md := fmt.Sprintf("![K线 #800px #400px](%s)", img.URL)

list, _ := c.Sender().ListMaterials(ctx, "")        // 有效图片列表（含 expires_at）
_ = c.Sender().DeleteMaterial(ctx, img.ImageID)     // 释放配额（单 bot 上限 100 张）
```

底层三步也可分开调用（`MaterialPrepare` → `MaterialPut` → `MaterialComplete`）。单图 ≤10MB、仅 png/jpeg/gif/webp，扩展名必须与 Content-Type 一致（本地先校验）。

## 消息事件结构化字段

消息类事件（C2C_MESSAGE_CREATE / GROUP_AT_MESSAGE_CREATE / GROUP_MESSAGE_CREATE）除 `Parts` 分段外，还解析了官方的三个结构化字段：

```go
ev, _ := event.Parse(payload)

ev.Mentions // []Sender：消息里 @ 的用户列表（平台保证不含 @ 机器人自身）
ev.Ark      // *event.ArkData：结构化卡片（message_type=3），ArkType=miniapp/map/feed...，
            //   Fields 里常见键 title/desc/jump_url/preview/source 等
ev.Elements // []event.MessageElement：msg_elements（message_type=103 引用消息时含被引用内容），
            //   递归嵌套，每个元素有 MsgIdx/Author/MessageType/Parts/Ark
```

`MessageType` 判别：0=文本、3=ARK 卡片（看 `ev.Ark`）、101=并行消息、102=聊天记录、103=引用消息（看 `ev.Elements` 与 `ev.RefMsgIdx`）。

非消息事件的关键字段（2026-08/09 文档新增）：

```go
ev.Timestamp        // 平台事件时间（ReceivedAt 是历史字段，取同一值）
ev.JoinRequest      // GROUP_JOIN_REQUEST：JoinRequestID / ApplySource / VerifyInfo / AutoApprovedStrategyID
ev.Interaction      // INTERACTION_CREATE 的 data.resolved：ButtonID / ButtonData / FeedbackOpt / AuthorizeData
ev.InteractionUserID // INTERACTION_CREATE 的操作人 openid（resolved.user_id → group_member_openid → user_openid）
ev.SubscribeResults // SUBSCRIBE_MESSAGE_STATUS：TemplateID / Op / SubscribeID ...
ev.FriendScene      // FRIEND_ADD 的场景值（1000/1001/...）
ev.SceneParam       // FRIEND_ADD 的 callback_data（配合 GenerateURLLink 做邀请来源归因）
```

> **按钮权限白名单**：`INTERACTION_CREATE` 的事件体**没有 author**。群场景操作人在
> `d.group_member_openid`，单聊在 `d.user_openid`——SDK 已把它们填进 `ev.Sender.ID` 与
> `ev.InteractionUserID`。**不要**用 `ev.Interaction.UserID` 做鉴权（官方标注它"仅频道场景有值"）。
> 单聊场景若 `ev.Sender.ID` 为空，说明你在用 v0.3.0 及更早版本，升级到 v0.3.1+。

> INTENT：WS 默认订阅 `GROUP_MEMBER_EVENT(1<<24) | GROUP_AND_C2C_EVENT(1<<25) | INTERACTION(1<<26)`
> （2026-11-03 起包含群成员/入群申请事件）。若机器人未被平台授予群成员事件权限，
> 订阅 1<<24 会被 4014 断开，`Run` 返回 `ws.ErrTerminalClose`；此时显式调小：
> `Intents: ws.IntentGroupAndC2CEvent | ws.IntentInteraction`。
> 分片：默认 `Shard{0,1}`；设 `Options.GatewayBot = true` 会走 `GET /gateway/bot`，
> 拿到平台建议分片数与 session 限额（建议数 > 当前配置时打 Warn），也可调
> `Transport.GatewayBot(ctx)` 自行读取。

## 重试与生命周期（v0.3.0 起）

- **网络错误 / 429**：自动重试（默认上限 3 次，指数退避 + 0~20% 抖动；429 优先用 `Retry-After`，
  且服务器给了等待时长时精确遵守不加抖动）。
- **重试可配**（v0.3.1）：`api.Config{Retry: &api.RetryConfig{MaxAttempts: -1}}` 关闭 SDK 内重试
  （交给宿主的持久化队列），`=1` 只试一次，`=0`/nil 默认 3 次；`Base` / `MaxRetryAfter` 也可调。
  门面 `qqbot.Config.Retry` 同样可用。
- **5xx**：只对幂等方法（GET/PUT/DELETE）自动重试；**非幂等 POST 默认不重发**（平台可能已处理，
  重发会重复发消息）。要恢复旧行为：`api.Config{RetryNonIdempotent: true}`。
- **响应体上限 4MB**：超限返回明确错误（不再静默截断成"JSON 解析失败"）。
- **正文长度**（v0.3.1）：`api.Config{MaxContentLen: 3500}` 在本地拦超长文本/Markdown，
  不发请求、不扣额度；默认 0 = 不校验。
  > **实测（2026-11-03）**：平台文档只给错误码 40054007「消息长度超限」、不给数值。真实机器人实测
  > **10,000 字符（ASCII 10KB / 中文 30KB）Markdown 主动消息均返回 200 且客户端完整显示**，
  > 未触发长度限制 ⇒ **平台侧没有 ~4000 的硬上限**。因此本地 `MaxContentLen` 的定位是
  > **展示体验**（消息太长没人看、客户端渲染压力），不是平台约束；建议 3000~4000。
- **配额日上限只计成功发送**：平台拒绝/超时/ctx 取消不占当日额度（否则一轮故障能打光当天额度）。
- **配额令牌失败退回**（v0.3.1）：主动消息在"平台接受（HTTP 2xx）之前"失败会退回两层桶令牌；
  2xx 之后（含响应体解析失败）不退——那条消息平台已收到。
- **401**：token 存储实现 `token.Deleter`（默认 `token.Memory` 已实现）时，自动作废缓存 →
  换新 token → 重试一次，不再一直失败到 TTL。
- **WS 关闭码**：`4001/4002/4010~4014/4914/4915` 视为不可恢复，`Run` 返回 `ws.ErrTerminalClose`
  （宿主应告警/退出）；`4006/4007/4900~4913` 先清 session 再 Identify；其余退避重连。
- `Run` 正常返回后再调用 `Run` 是安全的（返回 nil，不再 panic）。

## 错误处理

```go
_, err := c.Sender().Send(ctx, target, msg)
if errors.Is(err, apierr.ErrThrottledLocal) {
    // 本地主动配额拒绝：没调用平台、重试无意义 → 走兜底通道（如邮件）
}
var apiErr *qqbot.APIError
if errors.As(err, &apiErr) {
    switch {
    case apiErr.IsDuplicate():   // msg_id+msg_seq 重复，按成功处理
    case apiErr.IsExpired():     // 被动回复窗口/次数超限，降级
    case apiErr.IsRateLimited(): // 429/频控，退避
    case apiErr.IsAuth():        // 凭证/权限问题，不要原样重试
    }
}
```

`apierr.APIError.Code` 来自官方错误体的 **`err_code`**（历史 `code` 也兼容），`TraceID` 来自
`trace_id`，`HTTP` 是状态码。判错请用 `Code`，不要依赖 `message` 文本（官方明确 message 会变）。

## 开发

```bash
go build ./... && go vet ./... && go test -race ./...
```

依赖策略：除标准库外仅 WS 阶段引入 `gorilla/websocket`；配置/日志/重试都由使用方注入。

## 更新日志与升级

版本历史与每次发版的**接入方影响**（要不要跟着改、改什么）见 [CHANGELOG.md](CHANGELOG.md)；
包结构与新增功能的归属纪律见 `doc.go` 的模块地图。所有未标注 BREAKING 的变更均向后兼容。

## 与主项目的关系

主项目 `aichat` 依赖本仓库；本仓库刻意不 import 任何业务代码。
