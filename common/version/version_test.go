package version

import (
	"strings"
	"testing"
)

// TestVersionNonEmpty 版本号必须非空且不含空白：UA 里塞进空串或占位符会污染平台侧识别。
func TestVersionNonEmpty(t *testing.T) {
	v := Version()
	if v == "" || strings.ContainsAny(v, " \t\n") {
		t.Fatalf("版本号不合法：%q", v)
	}
}

// TestUserAgent UA 必须以产品名开头并带上版本，避免 UA 与实际版本脱节。
func TestUserAgent(t *testing.T) {
	if got, want := UserAgent(), "qqbot-sdk/"+Version(); got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}

// TestNormalize 构建元数据里的三种写法都要归一：v 前缀去掉，占位符落 devel。
func TestNormalize(t *testing.T) {
	cases := map[string]string{"v0.9.0": "0.9.0", "0.9.0": "0.9.0", "": "devel", "(devel)": "devel"}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
