// Package apierr 定义 QQ 开放平台的 API 错误与判定方法，根包与 api 包共用这一份。
package apierr

import (
	"errors"
	"fmt"
	"time"
)

// APIError 是平台返回的错误（HTTP 非 2xx 或业务错误码）。
//
// 判定方法把"哪些能重试、哪些该降级"编码成几行 if，业务侧不用背错误码表。
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
func (e *APIError) IsExpired() bool {
	switch e.Code {
	case 40034128, 304103, 40034005, 40034026:
		return true
	}
	return false
}

// IsAuth 表示凭证/权限类错误（token 失效、无权限），不要原样重试。
func (e *APIError) IsAuth() bool {
	switch e.Code {
	case 304004, 304036, 40034127, 40034105:
		return true
	}
	return false
}

// IsRateLimited 表示触发频控（平台错误码或 HTTP 429）。
// 40034100 是通用消息频控；50002 是流式消息接口的频率限制。
func (e *APIError) IsRateLimited() bool {
	return e.Code == 40034100 || e.Code == 50002 || e.HTTP == 429
}

// ErrThrottledLocal 表示【本地主动配额】拒绝：这条消息没有发出去，
// 也【没有调用平台】（调用前就在本地拦掉了，否则配额就没有省额度的意义）。
//
// ⚠️ 必须与平台 429（APIError.IsRateLimited）分得开，两者的处置完全不同：
//   - 本地拒绝：重试没有意义（额度是自己算出来的），应直接走兜底通道（如邮件）；
//   - 平台 429：说明本地配额口径与平台不一致，等 Retry-After 重试有意义。
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
