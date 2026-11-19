// Package qqtypes 是平台 DTO 与跨包公共解析（可被外部引用，不含业务字段）。
package qqtypes

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// User 是机器人自身信息（GET /users/@me）。
// 实测（2026-11-18）：share_url 恒有值，welcome_msg 可为空串，两者文档字段表未列、仅出现在响应示例；
// union_* 需特殊申请，默认不返回（事件体里的 union_* 恒为空，不要读）。
type User struct {
	ID       string `json:"id"`       // OpenID 格式，本 bot 自己的 id
	Username string `json:"username"` // 主昵称（唤醒词与 @ 文本匹配都用它）
	Avatar   string `json:"avatar"`
	Bot      bool   `json:"bot"` // 实测可能为 false，不要靠它判断自己
	// ShareURL 群分享链接（qun.qq.com/qunpro/robot/qunshare?...），用于把机器人分享进群。
	ShareURL string `json:"share_url"`
	// WelcomeMsg 进群欢迎语；未配置时为空串。
	WelcomeMsg string `json:"welcome_msg"`
}

// timeMilliThreshold 是"Unix 秒 vs 毫秒"启发式分界（>1e12 只可能是毫秒）。
const timeMilliThreshold = 1_000_000_000_000

// maxTimeSeconds 是 2100-01-01 的 Unix 秒：超出上限的时间戳视为脏数据（返回零值）。
// 没有这层夹紧，超大 float64 直接转 int64 会溢出成负数，产出年份荒谬的时间。
const (
	maxTimeSeconds = int64(4102444800)
	maxTimeMilli   = maxTimeSeconds * 1000
	// maxSafeFloat 是 float64 转 int64 的安全上限（2^63 附近会溢出）。
	maxSafeFloat = 9.2e18
)

// ParseTime 把平台时间戳归一化成 time.Time，兼容 RFC3339 / Unix 秒 / 毫秒三种形态。
// 空/无法识别返回零值（不当作错误）。
func ParseTime(raw json.RawMessage) time.Time {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return time.Time{}
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return time.Time{}
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
		// 字符串形式的 Unix 秒/毫秒（个别事件类型出现过）。
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return unixGuess(n)
		}
		return time.Time{}
	}
	// 裸数字：用 float64 承接（容忍小数秒，如 123.5；event/api 两处历史行为并集）。
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f <= 0 || f > maxSafeFloat {
		return time.Time{}
	}
	return unixGuess(int64(f))
}

// FirstNonEmpty 返回第一个非空（去空白后）字符串；全空返回 ""。
// 必须 TrimSpace 判空：配置里的 "  " 否则会顶掉真实值，导致鉴权/验签全失败。
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// unixGuess 按阈值猜秒或毫秒；超出合理区间返回零值。
func unixGuess(n int64) time.Time {
	if n > timeMilliThreshold {
		if n > maxTimeMilli {
			return time.Time{}
		}
		return time.UnixMilli(n)
	}
	if n > maxTimeSeconds {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
