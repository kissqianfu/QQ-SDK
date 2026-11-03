// Package api 是 QQ 开放平台 REST 客户端：access_token、发消息、自身信息。
//
// 只依赖标准库与 SDK 内的 apierr/token 包；token 刷新（提前 90s、串行）在这里完成。
//
// 包内模块边界（每文件一个域，互不越界）：
//   - client.go  构造与生命周期（Config / Client / New）
//   - token.go   access_token 缓存与刷新（401 时按 token.Deleter 主动失效）
//   - http.go    HTTP 传输细节（单次请求 / 重试骨架 / 退避 / 错误体解析 / 关闭码外的一切传输策略）
//   - message.go 发消息 / 撤回 / 输入中 / Me
//   - media.go   富媒体上传（URL / base64 / 分片，产出 file_info）
//   - material.go 素材管理（/v2/media-images，产出 qqbot-media:// 引用）
//   - stream.go  单聊流式回复
//   - panel.go   指令面板 CRUD
//   - menu.go    全局自定义菜单（仅 C2C）
//   - group.go   群管理查询（info / bot_state / restrict_chat_setting GET）
//   - group_admin.go 群治理写接口（禁言设置 / 成员 / 黑名单 / 入群申请）
//   - interaction.go 互动事件回应
//   - misc.go    机器人域零散接口（分享链接）
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
	// AuthURL 是换取 access_token 的地址。
	// 官方 2026-08-10 起把接口域名统一为 api.bot.qq.com，access_token 端点也随之；
	// 旧域名 https://bots.qq.com/app/getAppAccessToken 历史上可用，可用 Config.TokenURL 覆盖。
	AuthURL = "https://api.bot.qq.com/app/getAppAccessToken"
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

	// RetryNonIdempotent 为 true 时，非幂等请求（POST）遇到 5xx 也重试。
	// 默认 false：平台可能在返回 5xx 前已处理成功，重发会造成重复消息/重复资源。
	// 429 与网络错误不受本开关影响（始终重试）。
	RetryNonIdempotent bool

	// Retry 控制 REST 重试；nil = 默认（3 次尝试、100ms 基退避、Retry-After 上限 30s）。
	// 宿主已有持久化发送队列时建议 MaxAttempts<0，把重试交给队列（跨重启、可按优先级）。
	Retry *RetryConfig

	// MaxContentLen 是文本/Markdown 正文的最大字符数（rune 计，0 = 不校验）。
	// 平台超长会返回 40054007 且此时额度已消耗；本地先拦可省一次往返与一次扣额。
	MaxContentLen int

	// Quota 是出站主动消息三层配额；nil 表示用默认口径（群 60 QPM / 单聊 10 QPS /
	// 单关系 20 QPM / 每目标每日 1000 条）。QuotaConfig.Disable 可整体关闭。
	Quota *QuotaConfig
}

// RetryConfig 控制 REST 重试。
//
// MaxAttempts 语义（写死，避免零值歧义）：
//   - 0  = 用默认值 3
//   - 1  = 只尝试一次，失败立即返回
//   - <0 = 关闭 SDK 内重试（同样只尝试一次）：语义是"不在这里试，由宿主队列调度"。
//     不要把它改成 1 —— 两者行为相同但意图不同，读代码的人需要知道为什么只试一次。
//
// 注意：无论怎么配，非幂等 POST 的 5xx 默认仍不重试（见 Config.RetryNonIdempotent）。
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

		retryNonIdempotent: cfg.RetryNonIdempotent,
		maxContentLen:      cfg.MaxContentLen,

		retryAttempts: maxAttempts,
		retryBase:     retryBase,
		maxRetryAfter: maxRetryAfter,
	}
	// 重试参数归一化：0 = 默认；<0 = 关闭（只试一次）；1 = 只试一次。
	if cfg.Retry != nil {
		switch {
		case cfg.Retry.MaxAttempts < 0:
			c.retryAttempts = 1
		case cfg.Retry.MaxAttempts == 1:
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
