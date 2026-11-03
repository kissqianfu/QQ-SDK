package qqbot

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kissqianfu/QQ-SDK/api"
	"github.com/kissqianfu/QQ-SDK/token"
	"github.com/kissqianfu/QQ-SDK/transport"
)

// Intent 是平台 intents 位（订阅哪类事件）。
// 注意：intents 实际生效位置是 ws.Options.Intents（uint32，Transport 由业务构造）；
// 这组常量只作协议位文档与构造 ws.Options 时取值用，本包不再持有 intents 配置。
type Intent uint32

const (
	// IntentGroupAndC2C 订阅群聊与单聊消息事件。
	IntentGroupAndC2C Intent = 1 << 25
	// IntentInteraction 订阅按钮/指令回调事件。
	IntentInteraction Intent = 1 << 26
)

// Config 是 SDK 构造参数；New 只做校验，不建立连接。
type Config struct {
	AppID     string
	AppSecret string

	Transport transport.Transport // 接入通道；nil = 关闭（仅测试/纯 REST 场景）
	HTTP      *http.Client        // nil = 默认带连接池的 client
	Tokens    token.Store         // nil = 内存实现
	Logger    *slog.Logger        // nil = 静默

	// BaseURL / TokenURL 覆盖 REST 与 token 端点（沙箱/测试用）；空 = 官方正式地址。
	BaseURL  string
	TokenURL string

	// Quota 是出站主动消息三层配额；nil = 默认口径（群 60 QPM / 单聊 10 QPS /
	// 单关系 20 QPM / 每目标每日 1000 条）。QuotaConfig.Disable 可整体关闭。
	//
	// 放在门面层是为了让直接使用 qqbot.New 的宿主也能调节/关闭配额——
	// 之前只有裸用 api.New 才能传，门面反而成了"不可配置"。
	Quota *api.QuotaConfig

	// RetryNonIdempotent 为 true 时，非幂等请求（POST，如发消息）遇到 5xx 或传输层错误
	// 也自动重试。默认 false：平台可能已处理成功，重发会造成重复消息/重复资源。
	RetryNonIdempotent bool
}

// Validate 校验必填项；New 内部会调用。
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if strings.TrimSpace(c.AppID) == "" {
		add("AppID 不能为空")
	}
	if strings.TrimSpace(c.AppSecret) == "" {
		add("AppSecret 不能为空")
	}
	if len(errs) > 0 {
		return fmt.Errorf("qqbot: invalid config: %s", strings.Join(errs, "; "))
	}
	return nil
}
