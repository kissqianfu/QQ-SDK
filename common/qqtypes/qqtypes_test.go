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
	if !got.IsZero() {
		// 999999999999 秒是公元 3 万多年，属脏数据：必须夹成零值，不能产出荒谬时间。
		t.Fatalf("超出 2100 的秒级时间戳应判脏数据: %v", got)
	}
	// 合法秒级时间戳正常解析（2026-11-19T00:00:00Z）。
	got = ParseTime(json.RawMessage(`1795046400`))
	if got.Unix() != 1795046400 {
		t.Fatalf("合法秒级时间戳解析错误: %v", got)
	}
	// 超大 float64 转 int64 会溢出成负数：必须提前拦住。
	if got := ParseTime(json.RawMessage(`99999999999999999999`)); !got.IsZero() {
		t.Fatalf("超大时间戳应判脏数据: %v", got)
	}
}

// FirstNonEmpty 被各处用于"取第一个有效值"的兜底，纯空白必须当作空处理，
// 否则一份 "  " 会顶掉后面真实的 openid/群号。
func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"取第一个非空", []string{"", "a", "b"}, "a"},
		{"跳过纯空白", []string{"  ", "\t\n", "c"}, "c"},
		{"返回去空白后的值", []string{"  d  "}, "d"},
		{"全空返回空串", []string{"", "   "}, ""},
		{"无参数返回空串", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstNonEmpty(tt.in...); got != tt.want {
				t.Fatalf("FirstNonEmpty(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// EvictFIFO 是"键表到上限就淘汰"的共用实现：必须删掉最早入表且仍在表里的键，
// 并跳过已被外部清掉的键，否则淘汰会退化成空转、表继续涨。
func TestEvictFIFO(t *testing.T) {
	t.Run("淘汰最早的键", func(t *testing.T) {
		m := map[string]int{"a": 1, "b": 2, "c": 3}
		order := EvictFIFO(m, []string{"a", "b", "c"})
		if _, ok := m["a"]; ok {
			t.Fatal("a 应被淘汰")
		}
		if got, want := len(order), 2; got != want {
			t.Fatalf("剩余顺序表长度 = %d, want %d", got, want)
		}
		if order[0] != "b" || order[1] != "c" {
			t.Fatalf("顺序表 = %v, want [b c]", order)
		}
	})

	t.Run("跳过已不在表里的键", func(t *testing.T) {
		m := map[string]int{"c": 3}
		order := EvictFIFO(m, []string{"a", "b", "c"})
		if _, ok := m["c"]; ok {
			t.Fatal("c 应被淘汰（a、b 已不存在，跳过）")
		}
		if len(order) != 0 {
			t.Fatalf("顺序表 = %v, want 空", order)
		}
	})

	t.Run("空表只返回空顺序表", func(t *testing.T) {
		if got := EvictFIFO(map[string]int{}, nil); len(got) != 0 {
			t.Fatalf("顺序表 = %v, want 空", got)
		}
	})
}
