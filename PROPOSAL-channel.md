# 提案：门面内置双通道接入（ws / webhook）

> 来源：AlertBot 接入实践。目标：宿主不再自己拼 `ws.New` / `webhook.New`，
> 一个 `Config.Channel` 就能完成接入通道选择，事件统一走 `Events()`。
> **零值完全兼容现状，非 BREAKING**；以下均为建议，取舍权在 SDK 维护者。

## 1. Config 新增字段（config.go）

```go
// 内置接入通道标识。
const (
    ChannelWS      = "ws"
    ChannelWebhook = "webhook"
)

type Config struct {
    // ...现有字段不动...

    // Channel 选择内置接入通道："ws" / "webhook"；空 = 不内置（由 Transport 提供）。
    // 与 Transport 互斥：同时设置返回错误。选中后：
    //   ws      —— Run 阻塞运行（心跳/重连/Resume 内置，现状语义不变）；
    //   webhook —— 通过 client.WebhookHTTPHandler() 拿 http.Handler 挂到业务路由，
    //              Run 同样阻塞：ctx 取消或 Close 时停止接收、优雅收尾后返回。
    Channel string

    // WSOptions 仅 Channel=ws 时生效；nil = 默认（DefaultIntents + 内存 SessionStore）。
    // Token / AppID / Logger 由门面自动注入（Token 复用 REST 客户端缓存），业务不必填。
    WSOptions *ws.Options

    // WebhookOptions 仅 Channel=webhook 时生效；nil = 默认。
    // Secret / AppID / Logger 为空时自动回填 AppSecret / AppID / Logger；
    // OnEvent 由门面接管（事件经 Events() 消费），业务传入的 OnEvent 会被覆盖。
    WebhookOptions *webhook.Options
}
```

`Validate()` 扩展两条：

- `Channel` ∈ { "", ChannelWS, ChannelWebhook }，否则报错；
- `Channel != "" && Transport != nil` → 报错（互斥）。

## 2. 门面构造（client.go 的 New）

`apiClient` 构造完成后追加一步 `buildTransport`，成功则注入 `c.cfg.Transport`：

```go
func buildTransport(cfg Config, apiClient *api.Client) (transport.Transport, *webhook.Handler, error) {
    switch cfg.Channel {
    case "":
        return cfg.Transport, nil, nil // 现状
    case ChannelWS:
        opts := ws.Options{}
        if cfg.WSOptions != nil {
            opts = *cfg.WSOptions // 值拷贝，避免改调用方的结构体
        }
        if opts.AppID == "" {
            opts.AppID = cfg.AppID
        }
        opts.Token = apiClient.Token // 复用 REST 的 token 缓存与换票
        if opts.SessionStore == nil {
            opts.SessionStore = ws.NewMemorySession()
        }
        if opts.Logger == nil {
            opts.Logger = cfg.Logger
        }
        tr, err := ws.New(opts)
        return tr, nil, err
    case ChannelWebhook:
        opts := webhook.Options{}
        if cfg.WebhookOptions != nil {
            opts = *cfg.WebhookOptions
        }
        if opts.Secret == "" {
            opts.Secret = cfg.AppSecret
        }
        if opts.AppID == "" {
            opts.AppID = cfg.AppID
        }
        if opts.Logger == nil {
            opts.Logger = cfg.Logger
        }
        wt, err := newWebhookTransport(opts)
        if err != nil {
            return nil, nil, err
        }
        return wt, wt.h, nil
    default:
        return nil, nil, fmt.Errorf("qqbot: 未知 Channel %q", cfg.Channel)
    }
}
```

## 3. webhook → transport.Transport 桥接（新文件 channel.go）

webhook.Handler 是 http.Handler 不是 Transport，门面需要一个小适配器：
`OnEvent → 带缓冲 channel → Run 泵到 emit`，ctx 取消时先 `h.Close()`
（停止接收、等待已入队事件处理完）再把缓冲派发干净：

```go
// webhookTransport 把 webhook.Handler 桥接为 transport.Transport。
type webhookTransport struct {
    h    *webhook.Handler
    ch   chan *event.Event // 缓冲；满时 OnEvent 阻塞 → 上游队列反压（不会静默丢）
    done chan struct{}
}

func newWebhookTransport(opts webhook.Options) (*webhookTransport, error) {
    t := &webhookTransport{
        ch:   make(chan *event.Event, 256),
        done: make(chan struct{}),
    }
    opts.OnEvent = func(_ context.Context, ev *event.Event) {
        select {
        case t.ch <- ev:
        case <-t.done: // Run 已退出，丢弃
        }
    }
    h, err := webhook.New(opts)
    if err != nil {
        return nil, err
    }
    t.h = h
    return t, nil
}

func (t *webhookTransport) Run(ctx context.Context, emit func(*event.Event) error) error {
    defer close(t.done)
    for {
        select {
        case ev := <-t.ch:
            if err := emit(ev); err != nil {
                return err
            }
        case <-ctx.Done():
            t.h.Close() // 停止接收（新请求 503，平台重推），等待已入队事件
            for {       // 收尾：缓冲内事件派发完再返回
                select {
                case ev := <-t.ch:
                    _ = emit(ev)
                default:
                    return nil
                }
            }
        }
    }
}
```

## 4. Client 暴露 webhook 的 http.Handler（client.go）

Client 增加（仅 Channel=webhook 时非 nil）：

```go
// Client struct 新增字段
wh *webhook.Handler // webhook 模式的回调处理器；其余模式为 nil

// WebhookHTTPHandler 返回 webhook 回调处理器，业务挂到自己的路由（如 /alertbot/callback）。
// 非 webhook 模式返回 nil。应在启动 HTTP server 之前调用。
func (c *Client) WebhookHTTPHandler() http.Handler {
    if c.wh == nil {
        return nil
    }
    return c.wh
}
```

New 里把 `buildTransport` 返回的 `*webhook.Handler` 存进 `c.wh`。

## 5. 宿主（AlertBot）侧用法（改造后期望）

```go
c, _ := qqbot.New(qqbot.Config{
    AppID: appID, AppSecret: appSecret, Logger: log,
    Channel: "ws", // 或 "webhook"
    WSOptions: &ws.Options{ // ws 模式可调 intents
        Intents:      ws.IntentGroupAndC2CEvent | ws.IntentInteraction,
        SessionStore: ws.NewMemorySession(),
    },
})

// 统一消费事件（两通道一致）
go func() {
    for ev := range c.Events() {
        // 归一 / ACK / 业务……
    }
}()

// ws：阻塞运行；webhook：阻塞运行（停止时机 = ctx 取消或 Close）
go c.Run(ctx)

// webhook 模式才需要挂路由
if h := c.WebhookHTTPHandler(); h != nil {
    mux.Handle("/alertbot/callback", h)
}
```

## 6. 边界与注意事项

1. **缓冲满的反压语义**：`ch` 满时 OnEvent 阻塞，webhook 内部队列随后打满 →
   走其既有的丢弃统计（`Dropped()`/`OnDrop`），不会静默丢失或无界占内存。
2. **关闭语义**：webhook 模式 `Close()` → 取消 Run ctx → adapter 先 `h.Close()`
   再派发完缓冲 → facade 的 defer 关闭 Events()。与 ws 模式的收尾对称。
3. **依赖影响**：qqbot 门面 import `transport/ws` 与 `transport/webhook`；
   两者均无新第三方依赖（webhook 用标准库 crypto/ed25519）。不存在 import 环
   （api 不依赖 transport）。"拆卸"角度：只用 REST 的宿主可以继续直接用
   `api.New`，门面多出的只是两个内部包 import。
4. **测试建议**：
   - Channel 互斥 / 非法值报错（config 校验）；
   - ws 注入项：Token == apiClient.Token、SessionStore 默认、值拷贝不回写；
   - webhook：Secret/AppID/OnEvent 覆盖；Run → ctx 取消 → 缓冲派发完才返回；
   - WebhookHTTPHandler 非 nil / nil 分支。
5. **文档**：README「WebSocket 接入（推荐）」与「Webhook 接入」两节顶部补
   Channel 用法简介；CHANGELOG 记一条 Feature（建议 v0.3.2）。

## 7. 不建议做的

- 不要把事件归一（业务语义）做进 SDK——SDK 保持"平台语义、无业务字段"的边界。
- 不要让 webhook 模式绕过 Run：统一"Run 阻塞、Close 收尾"的心智模型，
  避免宿主按 channel 写两套生命周期代码。
