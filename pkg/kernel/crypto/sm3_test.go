package crypto_test

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/crypto"
)

// GB/T 32905-2016 SM3 标准测试向量。
func TestSM3StandardVectors(t *testing.T) {
	p := crypto.NewProvider(crypto.AlgoSM3)
	cases := []struct {
		in   string
		want string
	}{
		{"abc", "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"},
		// GB/T 32905-2016 附录 A 第二组：单分组 64 字节向量
		{strings.Repeat("abcd", 16), "debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732"},
	}
	for _, c := range cases {
		got, err := p.Hash(context.Background(), []byte(c.in))
		if err != nil {
			t.Fatalf("hash %q: %v", c.in[:10], err)
		}
		if hex.EncodeToString(got) != c.want {
			t.Fatalf("SM3(%q...) = %s, want %s", c.in[:10], hex.EncodeToString(got), c.want)
		}
	}
	if p.Algorithm() != "SM3" {
		t.Fatalf("algorithm = %q", p.Algorithm())
	}
}

func TestProviderSelection(t *testing.T) {
	if crypto.NewProvider(crypto.AlgoSHA256).Algorithm() != "SHA-256" {
		t.Fatal("sha256 provider selection broken")
	}
	if crypto.NewProvider(crypto.AlgoSM3).Algorithm() != "SM3" {
		t.Fatal("sm3 provider selection broken")
	}
	if crypto.NewProvider("unknown").Algorithm() != "SHA-256" {
		t.Fatal("unknown algo should fall back to SHA-256")
	}
}
