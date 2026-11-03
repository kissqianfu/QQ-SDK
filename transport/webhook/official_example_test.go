package webhook

// official_example_test.go 用官方文档《Webhook 方式》给出的完整样例做端到端校验：
// appid=11111111 secret=DG5g3B4j9X2KOErG plain_token=Arq0D5A61EgUu4OxUvOp
// event_ts=1725442341 → 官方期望签名 87befc99...c706。
//
// 这一条同时验证两件事：
//  1. seedFromSecret 是"repeat 到 32 字节"而不是零填充；
//  2. Ed25519 签名内容严格是 event_ts+plain_token（顺序、编码都不能差）。
//
// 与 httptest 自签名的用例不同，它的期望值来自官方文档，属于可离线复现的权威校验。

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func TestOfficialChallengeExample(t *testing.T) {
	const (
		officialSecret  = "DG5g3B4j9X2KOErG"
		officialToken   = "Arq0D5A61EgUu4OxUvOp"
		officialEventTS = "1725442341"
		officialSig     = "87befc99c42c651b3aac0278e71ada338433ae26fcb24307bdc5ad38c1adc2d01bcfcadc0842edac85e85205028a1132afe09280305f13aa6909ffc2d652c706"
	)
	key := ed25519.NewKeyFromSeed(seedFromSecret(officialSecret))
	got := hex.EncodeToString(ed25519.Sign(key, []byte(officialEventTS+officialToken)))
	if got != officialSig {
		t.Fatalf("签名与官方示例不一致：\n got  %s\n want %s", got, officialSig)
	}
}
