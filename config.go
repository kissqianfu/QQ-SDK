package qqbot

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kissqianfu/QQ-SDK/api"
	"github.com/kissqianfu/QQ-SDK/token"
	"github.com/kissqianfu/QQ-SDK/transport"
	"github.com/kissqianfu/QQ-SDK/transport/webhook"
	"github.com/kissqianfu/QQ-SDK/transport/ws"
)

// Intent 是平台 intents 位（订阅哪类事件）。
//
// ⚠️ 命名注意：本包的 "Channel/频道" 专指 QQ **频道（guild）**域，不在本 SDK 支持范围；
// 接入通道（ws/webhook）请用 Mode，别与频道混淆。
type Intent uint32

const (
	// IntentGroupAndC2C 订阅群聊与单聊消息事件。
	IntentGroupAndC2C Intent = 1 << 25
	// IntentInteraction 订阅按钮/指令回调事件。
	IntentInteraction Intent = 1 << 26
)

// 内置接入通道（Mode 取值）。
//
// 为什么叫 Mode 而不是 Channel：本 SDK 语境里 "Channel" 专指 QQ 频道（guild），
// 再拿它表示"ws/webhook 接入方式"会长期误导阅读者。
const (
	// ModeWS 使用内置 WebSocket 通道（心跳/重连/Resume 由 SDK 管理，Run 阻塞）。
	ModeWS = "ws"
	// ModeWebhook 使用内置 Webhook 通道：通过 WebhookHTTPHandler() 取 http.Handler
	// 挂到业务路由，Run 阻塞到 ctx 取消/Close（停止接收 → 优雅收尾后返回）。
	ModeWebhook = "webhook"
)

// Config 是 SDK 构造参数；New 只做校验，不建立连接。
type Config struct {
	AppID     string
	AppSecret string

	Transport transport.Transport // 自定义接入通道；nil = 无（仅测试/纯 REST 场景）。与 Mode 互斥
	HTTP      *http.Client        // nil = 默认带连接池的 client
	Tokens    token.Store         // nil = 内存实现
	Logger    *slog.Logger        // nil = 静默

	// BaseURL / TokenURL 覆盖 REST 与 token 端点（沙箱/测试用）；空 = 官方正式地址。
	BaseURL  string
	TokenURL string

	// Mode 选择内置接入通道："ws" / "webhook"；空 = 不内置（由 Transport 提供，兼容旧用法）。
	//
	//   - ModeWS：Run 阻塞运行，心跳/重连/Resume 内置；WSOptions 可调 intents 等。
	//   - ModeWebhook：WebhookHTTPHandler() 取回调处理器挂到业务路由；Run 阻塞到
	//     ctx 取消/Close，期间事件从 Events() 消费。
	//
	// 与 Transport 互斥：同时设置 New 直接报错。
	Mode string

	// WSOptions 仅 Mode=ws 时生效；nil = 默认。
	// AppID / Token / Logger 由门面自动回填（Token 复用 REST 客户端缓存与换票），业务不必填。
	// 默认注入 SessionStore（单进程内存实现），避免"断线即 Identify 丢窗口事件"。
	WSOptions *ws.Options

	// WebhookOptions 仅 Mode=webhook 时生效；nil = 默认。
	// Secret / AppID / Logger 为空时由门面回填；OnEvent 由门面接管（禁止自行设置，New 会报错）。
	WebhookOptions *webhook.Options

	// Quota 是出站主动消息三层配额；nil = 默认口径（群 60 QPM / 单聊 10 QPS /
	// 单关系 20 QPM / 每目标每日 1000 条）。QuotaConfig.Disable 可整体关闭。
	//
	// 放在门面层是为了让直接使用 qqbot.New 的宿主也能调节/关闭配额——
	// 之前只有裸用 api.New 才能传，门面反而成了"不可配置"。
	Quota *api.QuotaConfig

	// RetryNonIdempotent 为 true 时，非幂等请求（POST，如发消息）遇到 5xx 或传输层错误
	// 也自动重试。默认 false：平台可能已处理成功，重发会造成重复消息/重复资源。
	RetryNonIdempotent bool

	// Retry 控制 REST 重试；nil = 默认（3 次尝试 / 100ms 基退避 / Retry-After 上限 30s）。
	// 宿主已有持久化发送队列时，建议 MaxAttempts<0，把重试交给队列调度。
	Retry *api.RetryConfig

	// MaxContentLen 是文本/Markdown 正文上限（rune 计；0 = 不校验，建议 3500~4000）。
	MaxContentLen int
}

// Validate 校验必填项与 Mode/Transport 互斥关系；New 内部会调用。
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if strings.TrimSpace(c.AppID) == "" {
		add("AppID 不能为空")
	}
	if strings.TrimSpace(c.AppSecret) == "" {
		add("AppSecret 不能为空")
	}
	switch c.Mode {
	case "", ModeWS, ModeWebhook:
	default:
		add("Mode 仅支持 %q / %q（或空）", ModeWS, ModeWebhook)
	}
	if c.Mode != "" && c.Transport != nil {
		add("Mode 与 Transport 互斥：内置通道由 Mode 决定，自定义通道请留空 Mode")
	}
	// 选项与模式必须匹配：填错一边会导致"以为生效了其实被忽略"。
	if c.Mode != ModeWS && c.WSOptions != nil {
		add("WSOptions 仅在 Mode=%q 时生效（当前 Mode=%q）", ModeWS, c.Mode)
	}
	if c.Mode != ModeWebhook && c.WebhookOptions != nil {
		add("WebhookOptions 仅在 Mode=%q 时生效（当前 Mode=%q）", ModeWebhook, c.Mode)
	}
	if c.WebhookOptions != nil && c.WebhookOptions.OnEvent != nil {
		add("WebhookOptions.OnEvent 由门面接管（事件走 Events()），请勿自行设置")
	}
	if len(errs) > 0 {
		return fmt.Errorf("qqbot: invalid config: %s", strings.Join(errs, "; "))
	}
	return nil
}
