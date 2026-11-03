# 提案：被动回复锚点降级链（群聊互动 event_id 实测无效）

> **实施状态（2026-11-04，v0.3.3）**：已按修正版落地。与原文的差异以实现为准：
> 1. 方案 A 移到 **`api.Client.SendReply`**——`send` 是纯构造包、不做 IO，放 send 会与 api 成环；
> 2. 消息用 `build func() *send.Message` **每轮新建**；`msg_seq` 用 `send.SeqAllocator` **按候选锚点分别取号**；
> 3. **`40054005`（去重）当成功返回、绝不换锚点重发**（原文与 AlertBot 旧实现把它算降级，会重复发消息）；
> 4. 事件语义修正（**BREAKING**）：`Event.EventID` = 外层事件 id（锚点）；新增
>    `Event.InteractionID` = 裸 interaction_id（`Interact` 用）；
> 5. 新增 `Event.ReplyAnchors()`（纯函数候选链）与 `apierr.IsAnchorRejected()`；
> 6. 未验证项：webhook 模式外层信封 id 形态、单聊互动是否与 ws 群聊同规则，需真机各点一次按钮确认。
>
> 原文保留作设计背景。

> 来源：AlertBot 群聊按钮回调实践（2026-11-04）。目标：宿主不再自己拼
> "哪个 id 能当回复锚点"的候选链与降级逻辑。
> **全部为新增 API，零值完全兼容现状，非 BREAKING**；取舍权在 SDK 维护者。

## 问题（线上实测）

1. **群聊互动事件的裸 `interaction_id` 作 `event_id` 被动回复会被平台拒绝**：
   `POST /v2/groups/{group_openid}/messages` 返回
   `40034025 请求参数event_id无效`（事发与点击间隔 <1s，非过期问题）。
   SDK 现状（`event/parse.go`）对 INTERACTION_CREATE 优先填 `d.id`（裸 interaction_id，
   剥前缀）到 `ev.EventID`——这对 `/interactions` 回应接口是对的（实测可用），
   但**对消息被动回复接口是错的**；官方文档口径的 event_id 是
   "事件最外层的 id"（ws 封包 `env.ID`，形如 `INTERACTION_CREATE:xxx`）。
2. 宿主现可用的锚点其实有三个（`ev.ID` 外层、`ev.EventID` 裸 id、
   `ev.Interaction.MessageID` 被点按钮所在消息），但没有任何文档/API 提示
   谁有效、按什么顺序降级；AlertBot 只能把候选链 + 错误码降级
   （40034024/40034025/过期/重复才降级，其他错误直接返回）全写在业务层。

## 方案 A（最小面）：send 包新增锚点降级发送

```go
// ReplyAnchor 是一组被动回复锚点候选（按序尝试，首个送达即止）。
type ReplyAnchor struct {
    MsgID   string // 以 msg_id 被动回复（消息事件的 d.id；互动 resolved.message_id）
    EventID string // 以 event_id 被动回复（事件外层封包 id / interaction_id）
}

// ReplyToAnchorsWithSeq 按 anchors 顺序逐个尝试被动回复：
// 仅锚点类错误（40034024 msg_id 无效、40034025/40034026 event_id 无效/过期、
// 40054005 重复）降级到下一候选，其余错误直接返回；
// 每轮以全新消息构造（ReplyTo 会改写消息体，不可复用）。
// 全部候选失败时返回最后一个错误。
func (m *Message) ReplyToAnchorsWithSeq(ctx context.Context, seq SeqAllocator,
    anchors ...ReplyAnchor) (*Message, error)
```

## 方案 B（更省心）：Sender 按事件自动选锚点

```go
// Reply 从平台事件自动收集锚点并按序降级回复（内部走方案 A）：
//   消息事件：d.id（msg_id）
//   互动事件：外层封包 id（event_id）→ d.id（裸 interaction_id）→ resolved.message_id（msg_id）
// 事件无任何可用锚点时返回错误。
func (s *Sender) Reply(ctx context.Context, ev *event.Event, m *Message) (*Message, error)
```

## event 包配套（文档级）

- `Event` 已有 `ID`（= ws 封包最外层 id）与 `EventID`（互动=裸 interaction_id）：
  建议在 `EventID` 注释与 README 明确写清二者语义差异，并注明
  **群聊互动被动回复必须用 `Event.ID`（或 resolved.message_id），裸 interaction_id 会 40034025**。
- `InteractionData.MessageID` 注释补充："可作为被动回复 msg_id 兜底锚点"。

## 宿主侧参考实现

AlertBot（2026-11-04）已在业务层实现同语义降级链，可作对照：
`internal/qqevent/qqevent.go`（OuterID/ButtonMsgID 提取）+
`internal/channel/qq/engine.go` 的 `Reply`（候选链 + 锚点类错误码降级）。
SDK 若采纳，宿主可整体删掉这两段。
