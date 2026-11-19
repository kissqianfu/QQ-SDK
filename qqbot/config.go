package qqbot

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kissqianfu/QQ-SDK/common/token"
	"github.com/kissqianfu/QQ-SDK/core/api"
	"github.com/kissqianfu/QQ-SDK/core/transport"
	"github.com/kissqianfu/QQ-SDK/core/transport/webhook"
	"github.com/kissqianfu/QQ-SDK/core/transport/ws"
)

// 内置接入通道（Mode 取值）。
const (
	// ModeWS 使用内置 WebSocket 通道（心跳/重连/Resume 由 SDK 管理）。
	ModeWS = "ws"
	// ModeWebhook 使用内置 Webhook 通道（回调处理器经 WebhookHTTPHandler() 获取）。
	ModeWebhook = "webhook"
)

// Config 是 SDK 构造参数；New 只做校验，不建立连接。
type Config struct {
	AppID     string
	AppSecret string

	Transport transport.Transport // 自定义接入通道；nil = 无（仅测试/纯 REST 场景）。与 Mode 互斥
	HTTP      *http.Client        // nil = 默认带连接池的 client
	Tokens    token.Store         // nil = 内存实现
	Logger    *slog.Logger        // nil = 静默（内置通道注入 discard logger）

	// BaseURL / TokenURL 覆盖 REST 与 token 端点（沙箱/测试用）；空 = 官方正式地址。
	BaseURL  string
	TokenURL string

	// Mode 选择内置接入通道："ws" / "webhook"；空 = 不内置（由 Transport 提供）。与 Transport 互斥。
	Mode string

	// WSOptions 仅 Mode=ws 时生效；AppID/Token/HTTP/Logger 由门面自动回填。
	WSOptions *ws.Options

	// WebhookOptions 仅 Mode=webhook 时生效；Secret/AppID/Logger 由门面回填，
	// OnEvent 由门面接管（禁止自行设置）。
	WebhookOptions *webhook.Options

	// Quota 是出站主动消息三层配额；nil 用默认口径，QuotaConfig.Disable 可整体关闭。
	Quota *api.QuotaConfig

	// RetryNonIdempotent 为 true 时非幂等请求（POST）遇到 5xx 也重试（默认不重试）。
	RetryNonIdempotent bool

	// Retry 控制 REST 重试；nil = 默认（3 次尝试 / 100ms 基退避 / Retry-After 上限 30s）。
	Retry *api.RetryConfig

	// MaxContentLen 是文本/Markdown 正文上限（rune 计；0 = 不校验）。
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
	// 选项与模式必须匹配。
	if c.Mode != ModeWS && c.WSOptions != nil {
		add("WSOptions 仅在 Mode=%q 时生效（当前 Mode=%q）", ModeWS, c.Mode)
	}
	if c.Mode != ModeWebhook && c.WebhookOptions != nil {
		add("WebhookOptions 仅在 Mode=%q 时生效（当前 Mode=%q）", ModeWebhook, c.Mode)
	}
	if c.WebhookOptions != nil && c.WebhookOptions.OnEvent != nil {
		add("WebhookOptions.OnEvent 由门面接管（事件走 Events()），请勿自行设置")
	}
	// Token/Secret/AppID 由门面回填。
	if c.WSOptions != nil && c.WSOptions.Token != nil {
		add("WSOptions.Token 由门面回填（复用 REST token 缓存），请勿自行设置")
	}
	if c.WSOptions != nil && c.WSOptions.AppID != "" && strings.TrimSpace(c.WSOptions.AppID) != strings.TrimSpace(c.AppID) {
		add("WSOptions.AppID 必须为空或与 Config.AppID 一致")
	}
	if c.WebhookOptions != nil {
		if s := strings.TrimSpace(c.WebhookOptions.AppID); s != "" && s != strings.TrimSpace(c.AppID) {
			add("WebhookOptions.AppID 必须为空或与 Config.AppID 一致")
		}
		if s := strings.TrimSpace(c.WebhookOptions.Secret); s != "" && s != strings.TrimSpace(c.AppSecret) {
			// 验签只认 AppSecret，传别的值会让所有回调 401。
			add("WebhookOptions.Secret 必须为空或与 Config.AppSecret 一致")
		}
	}
	if c.MaxContentLen < 0 {
		add("MaxContentLen 不能为负数（0 = 不校验）")
	}
	if c.Retry != nil {
		if c.Retry.Base < 0 {
			add("Retry.Base 不能为负数")
		}
		if c.Retry.MaxRetryAfter < 0 {
			add("Retry.MaxRetryAfter 不能为负数")
		}
	}
	for name, raw := range map[string]string{"BaseURL": c.BaseURL, "TokenURL": c.TokenURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			add("%s 必须是绝对地址（当前 %q）", name, raw)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("qqbot: invalid config: %s", strings.Join(errs, "; "))
	}
	return nil
}
