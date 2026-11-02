// Package api 是 QQ 开放平台 REST 客户端：access_token、发消息、自身信息。
//
// 只依赖标准库与 SDK 内的 apierr/token 包；token 刷新（提前 90s、串行）在这里完成。
//
// 包内模块边界（每文件一个域，互不越界）：
//   - client.go  构造与生命周期（Config / Client / New）
//   - token.go   access_token 缓存与刷新
//   - http.go    HTTP 传输细节（单次请求 / 重试骨架 / 退避 / 错误体解析）
//   - message.go 发消息 / 撤回 / Me
//   - media.go   富媒体上传（URL / base64 / 分片）
//   - stream.go  单聊流式回复
//   - panel.go   指令面板 CRUD
//   - group.go   群管理查询（info / bot_state / restrict_chat_setting）
//   - interaction.go 互动事件回应
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kissqianfu/QQ-SDK/token"
)

const (
	// ProdBaseURL 是正式环境 REST 地址（2026-08 起官方统一域名）。
	// 旧地址 https://api.sgroup.qq.com 仍可用，可通过 Config.BaseURL 覆盖；沙箱为 sandbox.api.sgroup.qq.com。
	ProdBaseURL = "https://api.bot.qq.com"
	// AuthURL 是换取 access_token 的地址（注意不是 API 域名；bots.qq.com 与 api.bot.qq.com 均官方在列）。
	AuthURL = "https://bots.qq.com/app/getAppAccessToken"
)

// Config 是 REST 客户端参数；New 只做校验，不发请求。
type Config struct {
	AppID     string
	AppSecret string

	HTTP   *http.Client // nil = 默认（10s 超时 + 连接池）
	Tokens token.Store  // nil = 内存实现
	Logger *slog.Logger // nil = 静默

	// BaseURL / AuthURL 可覆盖（测试用）；空则用正式环境地址（本项目不接沙箱）。
	BaseURL  string
	TokenURL string
	// Sleep 可注入（测试用）；nil 用真实时间且等待 ctx 感知。
	Sleep func(time.Duration)

	// Quota 是出站主动消息三层配额；nil 表示用默认口径（群 60 QPM / 单聊 10 QPS /
	// 单关系 20 QPM / 每目标每日 1000 条）。QuotaConfig.Disable 可整体关闭。
	Quota *QuotaConfig
}

// Client 是 QQ 开放平台 REST 客户端。
type Client struct {
	baseURL  string
	tokenURL string
	appID    string
	secret   string

	http     *http.Client
	tokens   token.Store
	log      *slog.Logger
	sleepCtx func(ctx context.Context, d time.Duration) error
	quota    *quota // nil = 配额关闭（Config.Quota.Disable 或未构造）

	mu sync.Mutex // 串行刷新 token（单进程，不需要分布式锁）
}

// New 创建客户端并填默认值。
func New(cfg Config) (*Client, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("api: appID/appSecret 不能为空")
	}
	c := &Client{
		baseURL:  cfg.BaseURL,
		tokenURL: cfg.TokenURL,
		appID:    cfg.AppID,
		secret:   cfg.AppSecret,
		http:     cfg.HTTP,
		tokens:   cfg.Tokens,
		log:      cfg.Logger,
	}
	if c.baseURL == "" {
		c.baseURL = ProdBaseURL
	}
	if c.tokenURL == "" {
		c.tokenURL = AuthURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 10 * time.Second}
	}
	if c.tokens == nil {
		c.tokens = token.NewMemory()
	}
	if cfg.Sleep != nil {
		// 注入的 Sleep 不感知 ctx：等待后由调用方检查 ctx.Err()。
		c.sleepCtx = func(_ context.Context, d time.Duration) error {
			cfg.Sleep(d)
			return nil
		}
	} else {
		c.sleepCtx = waitCtx
	}
	// 配额默认开启：不显式指定就按官方口径兜底，宿主"忘了配"也不至于裸奔。
	// 关闭要显式写 Quota: &api.QuotaConfig{Disable: true}。
	if cfg.Quota == nil {
		cfg.Quota = &QuotaConfig{}
	}
	c.quota = newQuota(cfg.Quota)
	return c, nil
}
