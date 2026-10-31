// Package qqtypes 是平台 DTO 与跨包公共解析（可被外部引用，不含业务字段）。
package qqtypes

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// User 是机器人自身信息（GET /users/@me）。
//
// 不要读 union_openid / union_user_account：事件体里的这两个字段恒为空，
// 只有单独拉 member 信息时才提供。
type User struct {
	ID       string `json:"id"`       // OpenID 格式，本 bot 自己的 id
	Username string `json:"username"` // 主昵称（唤醒词与 @ 文本匹配都用它）
	Avatar   string `json:"avatar"`
	Bot      bool   `json:"bot"`
}

// timeMilliThreshold 是"Unix 秒 vs 毫秒"启发式分界：平台不同接口/事件的时间戳
// 出现过 RFC3339 字符串、Unix 秒、毫秒数字三种形态，>1e12 只可能是毫秒。
const timeMilliThreshold = 1_000_000_000_000

// ParseTime 把平台时间戳归一化成 time.Time，兼容三种官方形态：
// RFC3339 字符串（"2026-07-21T10:00:00+08:00"）、Unix 秒/毫秒数字、数字字符串。
// 空/null/无法识别返回零值（调用方用 IsZero 判断，不当作错误——解析失败不该丢事件）。
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
	if err := json.Unmarshal(raw, &f); err != nil || f <= 0 {
		return time.Time{}
	}
	return unixGuess(int64(f))
}

// unixGuess 按阈值猜秒或毫秒。
func unixGuess(n int64) time.Time {
	if n > timeMilliThreshold {
		return time.UnixMilli(n)
	}
	return time.Unix(n, 0)
}
