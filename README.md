# QQ-SDK

QQ 官方机器人接入层 SDK（Go）：零业务依赖、可整体拆卸替换。只做 QQ 单聊 + 群聊（频道域不支持）。

## 能力总览

**接入**

| 能力 | 明细 |
|---|---|
| 通道 | WebSocket（心跳判活 / 自动重连 / 关闭码三分类）、Webhook（`WebhookHTTPHandler` 交给业务路由）、自备 `Transport` |
| 鉴权 | `access_token` 自动获取与刷新（提前 90s、串行、401 重取一次） |
| 重试 | 幂等请求 5xx/429 遵守 `Retry-After`，否则线性退避（`base×(n-1)` + 0~20% 抖动）；非幂等 POST（发消息）默认不自动重发，避免重复投递（可配 `RetryNonIdempotent`） |
| 配额 | 三层本地拦截：群 60 QPM / 单聊 10 QPS / 单关系 20 QPM，另加每目标每日 1000 条 |
| 错误 | `apierr.APIError` 按官方 `err_code` 解析，判定 `IsDuplicate / IsExpired / IsAnchorRejected / IsAuth / IsRateLimited` |

**消息**

| 能力 | 入口 |
|---|---|
| 文本 / Markdown / 模板 / 图文卡片 / 富媒体 | `send.Text` `Markdown` `NewMarkdownTemplate` `NewCard` `Media` |
| 键盘按钮（含 Modal）、@全体、引用回复 | `.Keyboard` `.Reference` `.ReplyTo` `.ReplyToEvent` |
| 序号去重、唤醒、图片校验 | `.Seq` `.IsWakeup` `.ForceVerifyImageResource` |
| 发送 / 撤回 / 流式（仅单聊）/ 按钮回应 / 输入中状态 | `Send` `SendReply` `Recall` `StartStream` `Interact` `InputNotify` |

**群与素材**

| 能力 | 明细 |
|---|---|
| 群信息与禁言 | `GroupInfo`（含 `union_group_openid`）`GroupBotState` `GroupRestrictSetting` `SetGroupMemberMute`（≤20 人/次，≤30 天） |
| 成员与黑名单 | 列表（含 `All`）、详情、批量移除（≤20，可同时拉黑）、黑名单增删 |
| 入群审批 | 申请列表（≤50）、`ApprovalJoinRequest`（approve / decline + 拉黑） |
| 自动审批策略 | 策略增删改查、全量扫描、白名单（≤1 万/次，`All` 自动分批） |
| 富媒体与素材 | 直传 / 分片（`upload_prepare` → `FinishPart` → `MergeUpload`）、`media-images` prepare / complete / delete |

**面板、菜单与机器人**：`ListPanels`（含 `All`）/ `PanelDetail` / `Create` / `Update` / `Delete` / `UpdatePanelTargets`；`GetMenu` / `PutMenu`（全量覆盖）/ `DeleteMenu`；`Me` / `GenerateURLLink`。

**用户**：`UserInfo`（用户名 / 头像 / 是否接收主动推送 / 跨应用 openid，10 QPS；非好友返回 40184001）。

**事件（16 种）**：`C2C_MESSAGE_CREATE` `C2C_MSG_RECEIVE` `C2C_MSG_REJECT` `GROUP_AT_MESSAGE_CREATE` `GROUP_MESSAGE_CREATE` `GROUP_MSG_RECEIVE` `GROUP_MSG_REJECT` `GROUP_ADD_ROBOT` `GROUP_DEL_ROBOT` `GROUP_MEMBER_ADD` `GROUP_MEMBER_REMOVE` `GROUP_JOIN_REQUEST`（含 `auto_approved.strategy_id`）`FRIEND_ADD` `FRIEND_DEL` `SUBSCRIBE_MESSAGE_STATUS` `INTERACTION_CREATE`。消息事件 `Sender` 携带头像与群内昵称，群事件带 `UnionGroupOpenID`。

## 设计取舍

- **零业务依赖**：除标准库外仅 WS 阶段引入 `gorilla/websocket`；配置、日志、凭证存储全由使用方注入。
- **配额在本地拦**：超限直接返 `apierr.ErrThrottledLocal`，不消耗平台侧的惩罚额度。
- **失败语义可判**：重复消息、锚点过期、限流、鉴权失效各有判定方法，不用比对字符串。
- **锚点显式传**：被动回复的 `msg_id` / `event_id` 二选一不猜；`SendReply` 在锚点被拒时自动降级。

## 安装

```bash
go get github.com/kissqianfu/QQ-SDK
```

构建工具链要求 **Go ≥ 1.27.4**：`GO-2026-6859`（1.27.3 及之前 stdlib `net/http.MaxBytesReader` 头部竞态，
可打崩 HTTP/1 server）只在标准库侧修复，webhook 回调路径会用到该方法，SDK 代码层无法规避。

## 版本

版本只由 git tag 表达（`vX.Y.Z`），源码内不落版本文件：`version.Version()` 从构建元数据取值
（即 `go.mod` 里钉的 tag，仓库内直跑为 `devel`），请求头 UA 由 `version.UserAgent()` 给出（`qqbot-sdk/<版本>`）。

## 最简用法

```go
c, _ := qqbot.New(qqbot.Config{
    AppID: appID, AppSecret: appSecret,
    Mode: qqbot.ModeWS, // 或 qqbot.ModeWebhook
})

go func() {
    for ev := range c.Events() {
        if ev.Type == event.GroupAtMessageCreate {
            _, _ = c.Sender().Send(ctx, api.Target{Scene: ev.Scene, ID: ev.TargetID},
                send.Text("hi").ReplyTo(ev.MsgID, 1))
        }
        if ev.Type == event.InteractionCreate {
            // 按钮必须回应；回消息用 SendReply（锚点被拒自动降级）
            _ = c.Sender().Interact(ctx, ev.InteractionID, api.InteractOK)
        }
    }
}()
go c.Run(ctx) // 阻塞到 ctx 取消 / Close

// 仅 webhook 模式：把回调处理器挂到业务路由（SDK 不替业务起 server）
if h := c.WebhookHTTPHandler(); h != nil {
    mux.Handle("/callback", h)
}
```

`c.Sender()` 返回 `*api.Client`，上面表里的 REST 能力都在它上面。

## 开发

```bash
go build ./... && go vet ./... && go test -race ./...
```
