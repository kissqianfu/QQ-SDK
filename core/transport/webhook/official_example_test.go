package webhook

// official_example_test.go 用官方文档《Webhook 方式》的完整样例做端到端校验
// （期望签名来自官方文档，同时验证 seed repeat 到 32 字节、签名内容为 event_ts+plain_token）。

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
