// Package apierr 定义 QQ 开放平台的 API 错误与判定方法，api / qqbot 共用这一份。
package apierr

import (
	"errors"
	"fmt"
	"time"
)

// APIError 是平台返回的错误（HTTP 非 2xx 或业务错误码）。
type APIError struct {
	Code    int // 平台错误码，如 40054005；0 表示只有 HTTP 状态
	Message string
	HTTP    int
	TraceID string
}

// Error 实现 error。
func (e *APIError) Error() string {
	return fmt.Sprintf("qqbot: api error code=%d http=%d msg=%s trace=%s", e.Code, e.HTTP, e.Message, e.TraceID)
}

// IsDuplicate 表示平台按 msg_id + msg_seq 判定重复（已发出过），业务应按成功处理。
func (e *APIError) IsDuplicate() bool { return e.Code == 40054005 }

// IsExpired 表示被动回复窗口/次数超限，或消息/事件锚点已过期。
// 口径 = 锚点被拒全集（见 IsAnchorRejected）∪ 40034027（event_id 次数超限——
// 锚点本身未失效但本周期已用尽，同样"过期"不可原样重试）。单一事实来源，避免两处码表漂移。
func (e *APIError) IsExpired() bool {
	return e.IsAnchorRejected() || e.Code == 40034027
}

// IsAuth 表示凭证/权限类错误（token 失效、无权限、HTTP 401），不要原样重试。
func (e *APIError) IsAuth() bool {
	if e.HTTP == 401 {
		return true
	}
	switch e.Code {
	case 11241, 11242, 11243, 11251, 11252, 11253, 11254, 11261, 11262, 11263, 11264, 11265,
		11273, 11274, 11275, 11281, 11282,
		304004, 304036, 40034105, 40034127:
		return true
	}
	return false
}

// IsAnchorRejected 表示被动回复锚点被平台明确拒绝且消息未送达，可换下一个锚点重发。
// 刻意不含 40054005（去重=已成功），换锚点重发会使用户收到两条。
func (e *APIError) IsAnchorRejected() bool {
	switch e.Code {
	case 40034024, 40034025, 40034026, 40034005, 304103, 40034128:
		return true
	}
	return false
}

// IsRateLimited 表示触发频控（平台错误码或 HTTP 429）。
// 40034100 是通用消息频控；50002 是流式消息接口的频率限制。
func (e *APIError) IsRateLimited() bool {
	return e.Code == 40034100 || e.Code == 50002 || e.HTTP == 429
}

// ErrThrottledLocal 表示本地主动配额拒绝（未调用平台）；必须与平台 429 分开处置：
// 本地拒绝重试无意义，平台 429 等 Retry-After 重试有意义。
var ErrThrottledLocal = errors.New("qqbot: 本地主动配额已满")

// ThrottleError 是本地主动配额拒绝的详细错误：Layer 供宿主导出分标签指标，
// RetryAfter 是该层预计多久后腾出额度（日上限为 0，表示要等下一个日界）。
type ThrottleError struct {
	Layer      string // group / single / relation / daily
	RetryAfter time.Duration
}

// Error 实现 error。
func (e *ThrottleError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("qqbot: 本地主动配额已满 layer=%s retry_after=%s", e.Layer, e.RetryAfter)
	}
	return fmt.Sprintf("qqbot: 本地主动配额已满 layer=%s", e.Layer)
}

// Is 让 errors.Is(err, ErrThrottledLocal) 成立，业务不必记住具体类型。
func (e *ThrottleError) Is(target error) bool { return target == ErrThrottledLocal }
