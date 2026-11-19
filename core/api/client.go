// Package api 是 QQ 开放平台 REST 客户端：access_token、发消息、富媒体、群管理等。
// 依赖标准库与 SDK 内的 apierr/token/qqtypes/event/send 包；token 刷新（提前 90s、串行）在这里完成。
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kissqianfu/QQ-SDK/common/token"
)

const (
	// ProdBaseURL 是正式环境 REST 地址（旧地址 api.sgroup.qq.com 可用 Config.BaseURL 覆盖）。
	ProdBaseURL = "https://api.bot.qq.com"
	// AuthURL 是换取 access_token 的地址（旧域名可用 Config.TokenURL 覆盖）。
	AuthURL = "https://api.bot.qq.com/app/getAppAccessToken"
)

// Config 是 REST 客户端参数；New 只做校验，不发请求。
type Config struct {
	AppID     string
	AppSecret string

	HTTP   *http.Client // nil = 默认（10s 超时 + 连接池）
	Tokens token.Store  // nil = 内存实现
	Logger *slog.Logger // nil = 静默

	// BaseURL / TokenURL 可覆盖（测试用）；空则用正式环境地址。
	BaseURL  string
	TokenURL string
	// Sleep 可注入时钟（测试用）；nil 用真实时间。
	Sleep func(time.Duration)

	// RetryNonIdempotent 为 true 时非幂等请求（POST）遇到 5xx 也重试（默认不重试）。
	RetryNonIdempotent bool

	// Retry 控制 REST 重试；nil = 默认（3 次尝试、100ms 基退避、Retry-After 上限 30s）。
	Retry *RetryConfig

	// MaxContentLen 是文本/Markdown 正文的最大字符数（rune 计，0 = 不校验）。
	MaxContentLen int

	// Quota 是出站主动消息三层配额；nil 用默认口径，QuotaConfig.Disable 可整体关闭。
	Quota *QuotaConfig
}

// RetryConfig 控制 REST 重试；MaxAttempts：0=默认 3，1=只试一次，<0=关闭 SDK 内重试。
// 无论怎么配，非幂等 POST 的 5xx 默认仍不重试。
type RetryConfig struct {
	MaxAttempts   int
	Base          time.Duration // 退避基数，默认 100ms
	MaxRetryAfter time.Duration // Retry-After 等待上限，默认 30s
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

	// retryNonIdempotent 见 Config.RetryNonIdempotent。
	retryNonIdempotent bool
	// 重试参数（Config.Retry 归一化后）。
	retryAttempts int
	retryBase     time.Duration
	maxRetryAfter time.Duration
	// maxContentLen 见 Config.MaxContentLen（0 = 不校验）。
	maxContentLen int

	mu       sync.Mutex     // 保护 inflight 登记
	inflight *refreshFlight // 进行中的 token 刷新（手写 singleflight，I/O 在锁外）
}

// Close 释放空闲连接（不动调用方注入的 http.Client，也可继续使用）。
func (c *Client) Close() error {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
	return nil
}

// uploadHTTP 返回二进制直传用的 client（清零 Timeout，慢链路时长交给 ctx 控制）。
func (c *Client) uploadHTTP() *http.Client {
	if c.http.Timeout == 0 {
		return c.http
	}
	noTimeout := *c.http
	noTimeout.Timeout = 0
	return &noTimeout
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

		retryNonIdempotent: cfg.RetryNonIdempotent,
		maxContentLen:      cfg.MaxContentLen,

		retryAttempts: maxAttempts,
		retryBase:     retryBase,
		maxRetryAfter: maxRetryAfter,
	}
	// 重试参数归一化：0 = 默认；<0 = 关闭（只试一次）；1 = 只试一次。
	if cfg.Retry != nil {
		switch {
		case cfg.Retry.MaxAttempts < 0, cfg.Retry.MaxAttempts == 1:
			// 负数 = 关闭重试；1 = 只试一次。
			c.retryAttempts = 1
		case cfg.Retry.MaxAttempts > 1:
			c.retryAttempts = cfg.Retry.MaxAttempts
		}
		if cfg.Retry.Base > 0 {
			c.retryBase = cfg.Retry.Base
		}
		if cfg.Retry.MaxRetryAfter > 0 {
			c.maxRetryAfter = cfg.Retry.MaxRetryAfter
		}
	}
	if c.baseURL == "" {
		c.baseURL = ProdBaseURL
	}
	if c.tokenURL == "" {
		c.tokenURL = AuthURL
	}
	// 统一去掉基址尾斜杠，避免拼出 //v2/... 这类路径。
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	c.tokenURL = strings.TrimRight(c.tokenURL, "/")
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
	// 配额默认开启：关闭要显式写 Quota: &api.QuotaConfig{Disable: true}。
	if cfg.Quota == nil {
		cfg.Quota = &QuotaConfig{}
	}
	c.quota = newQuota(cfg.Quota)
	return c, nil
}
