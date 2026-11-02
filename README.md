# QQ-SDK

QQ 官方机器人接入层 SDK（Go）：零业务依赖、可整体拆卸替换。

- 接入：WebSocket + Webhook 双通道（`Transport` 接口，SDK 不替业务起 server）
- REST：access_token 刷新（提前 90s、互斥）、群/单聊发消息、`Me`（`GET /users/@me`）、富媒体上传、互动事件响应（`PUT /interactions/{id}`）
- 消息：文本 / 原生 Markdown / **Markdown 模板**（`custom_template_id`+params）/ 富媒体 / 图文卡片；键盘支持自定义与**模板短形式**（`{"keyboard":{"id":...}}`）；被动回复锚点支持 `msg_id` 与 `event_id` 二选一
- 协议正确性内置：`send.Message` 构造器做本地校验（卡片 title≤50 / url 必须 https / 键盘行列上限等）
- 事件：平台语义的 `event.Event`（无业务字段）
- 错误：`apierr.APIError` 判定方法（`IsDuplicate` / `IsExpired` / `IsAuth` / `IsRateLimited`）
- 稳健性内置：WS 心跳判活（读空闲超时自动重连）、指数退避（稳定连接断线后重置）、429 `Retry-After`、ctx 取消即停重试

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
        // SessionStore: 自选，持久化 session/seq 用于 Resume
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

## 错误处理

```go
_, err := c.Sender().Send(ctx, target, msg)
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
