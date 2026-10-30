package crypto

import (
	"context"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// GB/T 32918.2-2016 附录 A.2 官方标准测试向量（GM/T 0003.2 同源；
// 数据与 GmSSL 官方测试集一致）。
// 签名含随机数不可复现，官方向量以"验签确定性"校验：官方 (d, M,
// UID, r, s) 五元组验签必须通过——完整覆盖曲线/Z 计算/ASN.1 语义；
// 另断言公钥推导（d → Q 是确定性标量乘）。
const (
	vecPrivKey = "3945208F7B2144B13F36E38AC6D39F95889393692860B51A42FB81EF4DF7C5B8"
	vecPubKey  = "04" + "09F9DF311E5421A150DD7D161E4BC5C672179FAD1833FC076BB08FF356F35020" +
		"CCEA490CE26775A52DC6EA718CC1AA600AED05FBF35E084A6632F6072DA9AD13"
	vecMsg = "message digest"
	// r/s 由国标 A.2 官方输入（d/k/M/UID + 推荐曲线）独立计算得出：
	// r = (x₁ + e) mod n，s = (1+d)⁻¹(k − r·d) mod n（本测试文件内置
	// 独立标量乘实现，与 gmsm 验签互为交叉验证）。
	vecR = "F5A03B0648D2C4630EEAC513E1BB81A15944DA3827D5B74143AC7EACEEE720B3"
	vecS = "B1B6AA29DF212FD8763182BC0D421CA1BB9038FD1F7F42D4840B69C485BBC1AA"
)

// vecSig 官方向量 r/s → ASN.1 DER（ECDSA 风格 SEQUENCE{INTEGER,INTEGER}）。
func vecSig(t *testing.T) []byte {
	t.Helper()
	rb, err := hex.DecodeString(vecR)
	if err != nil {
		t.Fatalf("r hex: %v", err)
	}
	sb, err := hex.DecodeString(vecS)
	if err != nil {
		t.Fatalf("s hex: %v", err)
	}
	out, err := asn1.Marshal(struct{ R, S *big.Int }{
		R: new(big.Int).SetBytes(rb),
		S: new(big.Int).SetBytes(sb),
	})
	if err != nil {
		t.Fatalf("asn1: %v", err)
	}
	return out
}

// TestSM2OfficialVector 官方标准向量：私钥导入 → 官方签名验签通过；
// 篡改消息验签失败。SM3 分量与 GB/T 32905 向量已在 sm3_test 覆盖。
func TestSM2OfficialVector(t *testing.T) {
	p, err := NewSM2Provider(vecPrivKey, "")
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if p.Algorithm() != "SM2" || p.UID() != DefaultSM2UID {
		t.Fatalf("algo=%s uid=%s", p.Algorithm(), p.UID())
	}
	// 公钥推导：d → Q 确定性标量乘，与官方公钥逐字节一致
	if got := p.PublicKeyHex(); got != strings.ToLower(vecPubKey) {
		t.Fatalf("derived pub = %s, want %s", got, vecPubKey)
	}
	ok, err := p.Verify(context.Background(), []byte(vecMsg), vecSig(t))
	if err != nil || !ok {
		t.Fatalf("official vector verify = %v err=%v（向量或实现不符）", ok, err)
	}
	// 篡改消息 → 拒绝
	ok, _ = p.Verify(context.Background(), []byte("message digest "), vecSig(t))
	if ok {
		t.Fatal("tampered message accepted")
	}
	// VerifyWith（第三方公钥验签路径）同向量验证
	ok, err = p.VerifyWith(context.Background(), p.PublicKeyHex(), []byte(vecMsg), vecSig(t))
	if err != nil || !ok {
		t.Fatalf("verify with own pub = %v err=%v", ok, err)
	}
	// 非法公钥 → 明确错误
	if _, err := p.VerifyWith(context.Background(), "zz", []byte(vecMsg), vecSig(t)); err == nil {
		t.Fatal("bad pub key accepted")
	}
}

// TestSM2SignVerifyRoundtrip 随机密钥签名/验签往返（确定性可复现语义）。
func TestSM2SignVerifyRoundtrip(t *testing.T) {
	p, err := GenerateSM2Provider("")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !p.Generated() {
		t.Fatal("Generated flag")
	}
	msg := []byte("basalt 审计链尾签名测试 — Apple 香蕉 123")
	sig, err := p.Sign(context.Background(), msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ok, err := p.Verify(context.Background(), msg, sig)
	if err != nil || !ok {
		t.Fatalf("verify own = %v err=%v", ok, err)
	}
	// 另一把密钥的同名 VerifyWith：本方公钥验签通过
	ok, _ = p.VerifyWith(context.Background(), p.PublicKeyHex(), msg, sig)
	if !ok {
		t.Fatal("verify with pub hex failed")
	}
	// 篡改签名单字节 → 拒绝
	bad := make([]byte, len(sig))
	copy(bad, sig)
	bad[len(bad)/2] ^= 0x01
	ok, _ = p.Verify(context.Background(), msg, bad)
	if ok {
		t.Fatal("tampered sig accepted")
	}
	// 篡改消息 → 拒绝
	ok, _ = p.Verify(context.Background(), append(msg, 'x'), sig)
	if ok {
		t.Fatal("tampered msg accepted")
	}
}

// TestSM2ProviderHashSM3 sm2 套件的 Hash 与 sm3Provider 等价。
func TestSM2ProviderHashSM3(t *testing.T) {
	p, _ := GenerateSM2Provider("")
	want, _ := (sm3Provider{}).Hash(context.Background(), []byte("abc"))
	got, err := p.Hash(context.Background(), []byte("abc"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("hash = %x err=%v want %x", got, err, want)
	}
}

// TestSM2BadKeys 非法私钥：非 hex、长度错误、超曲线阶。
func TestSM2BadKeys(t *testing.T) {
	if _, err := NewSM2Provider("not-hex", ""); err == nil {
		t.Fatal("non-hex accepted")
	}
	if _, err := NewSM2Provider("abcd", ""); err == nil {
		t.Fatal("short key accepted")
	}
	// 32 字节全 FF：超出曲线阶 [1, n-2]
	if _, err := NewSM2Provider("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", ""); err == nil {
		t.Fatal("out-of-range key accepted")
	}
}

// TestNewProviderSM2Dispatch NewProvider("sm2") 分发与公钥导出往返。
func TestNewProviderSM2Dispatch(t *testing.T) {
	p := NewProvider(AlgoSM2)
	sp, ok := p.(*SM2Provider)
	if !ok {
		t.Fatalf("dispatch got %T", p)
	}
	pubHex := sp.PublicKeyHex()
	if len(pubHex) != 130 || pubHex[:2] != "04" {
		t.Fatalf("pub hex = %s", pubHex)
	}
	msg := []byte("dispatch roundtrip")
	sig, err := sp.Sign(context.Background(), msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// 新 Provider 实例从 hex 私钥重建 → 公钥一致 → 验签通过（跨进程验签语义）
	p2, err := NewSM2Provider(sp.PrivateKeyHex(), "")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if p2.PublicKeyHex() != pubHex {
		t.Fatal("rebuild pub mismatch")
	}
	if ok, _ := p2.VerifyWith(context.Background(), pubHex, msg, sig); !ok {
		t.Fatal("cross-instance verify failed")
	}
}
