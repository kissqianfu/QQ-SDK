package qqbot

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

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
