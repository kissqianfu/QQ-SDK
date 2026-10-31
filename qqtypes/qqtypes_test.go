package qqtypes

import (
	"encoding/json"
	"testing"
	"time"
)

// ParseTime 是 event/api 共用的时间戳归一化模块：三形态 + 非法输入护栏。
func TestParseTime(t *testing.T) {
	zone, _ := time.Parse(time.RFC3339, "2026-07-21T10:00:00+08:00")
	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{"RFC3339 字符串", `"2026-07-21T10:00:00+08:00"`, zone},
		{"Unix 毫秒数字", `1793400000000`, time.UnixMilli(1793400000000)},
		{"Unix 秒数字", `1793400000`, time.Unix(1793400000, 0)},
		{"毫秒数字字符串", `"1793400000000"`, time.UnixMilli(1793400000000)},
		{"秒数字字符串", `"1793400000"`, time.Unix(1793400000, 0)},
		{"空", ``, time.Time{}},
		{"null", `null`, time.Time{}},
		{"空字符串", `""`, time.Time{}},
		{"非法字符串", `"abc"`, time.Time{}},
		{"非正数", `0`, time.Time{}},
		{"负数", `-5`, time.Time{}},
		{"非 JSON", `abc`, time.Time{}},
		{"对象", `{}`, time.Time{}},
		{"布尔", `true`, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTime(json.RawMessage(tt.raw))
			if !got.Equal(tt.want) {
				t.Fatalf("ParseTime(%s) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseTimeThreshold(t *testing.T) {
	// 阈值边界：1e12 整只可能毫秒（1970-01-12），再小就是 3 万多年的秒——判毫秒。
	got := ParseTime(json.RawMessage(`1000000000001`))
	if got.UnixMilli() != 1000000000001 {
		t.Fatalf("阈值上方应判毫秒: %v", got)
	}
	got = ParseTime(json.RawMessage(`999999999999`))
	if got.Unix() != 999999999999 {
		t.Fatalf("阈值下方应判秒: %v", got)
	}
}
