// Package file 内的 S3 兼容驱动：AWS Signature V4 自研实现。
//
// 选型说明：任务约束"不引重量级 SDK"——aws-sdk-go-v2 全家桶与
// minio-go 均为重依赖（前者依赖树 50+ 包，后者携带自有 JSON/uuid
// 等）。S3 协议面收敛在 PUT/GET/DELETE/ListObjectsV2/预签名五操作，
// SigV4 签名约 200 行标准库实现完全覆盖；MinIO/阿里云 OSS/腾讯云
// COS 均提供 S3 兼容端点（endpoint 配置见 docs/file-s3.md），七牛
// 等非 S3 协议云经 Storage 接口缝后续接入。
package file

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// sigV4 是一次请求的签名上下文。
type sigV4 struct {
	accessKey, secretKey string
	region, service      string
}

const sigV4Algorithm = "AWS4-HMAC-SHA256"

// hmacSHA256 SigV4 全链使用的 HMAC。
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// signingKey 派生签名密钥（HMAC 链）。
func (v sigV4) signingKey(t time.Time) []byte {
	kDate := hmacSHA256([]byte("AWS4"+v.secretKey), t.UTC().Format("20060102"))
	kRegion := hmacSHA256(kDate, v.region)
	kService := hmacSHA256(kRegion, v.service)
	return hmacSHA256(kService, "aws4_request")
}

// scope 签名范围串。
func (v sigV4) scope(t time.Time) string {
	return fmt.Sprintf("%s/%s/%s/aws4_request", t.UTC().Format("20060102"), v.region, v.service)
}

// canonicalURI S3 对象键的规范 URI：每段 RFC3986 编码、斜杠保留。
func canonicalURI(key string) string {
	segs := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(segs, "/")
}

// canonicalQuery 规范查询串：键值排序 + RFC3986 编码。
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(q.Get(k)))
	}
	return b.String()
}

// sign 计算请求头签名。host 必填；extraHeaders 参与签名的附加头
// （小写化；host 已含）。
func (v sigV4) sign(method, rawURL string, headers map[string]string, payloadHash string, t time.Time) string {
	u, _ := url.Parse(rawURL)
	signedHeaders, canonicalHeaders := canonicalizeHeaders(headers)

	creq := strings.Join([]string{
		method,
		canonicalURI(u.Path),
		canonicalQuery(u.Query()),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	sts := strings.Join([]string{
		sigV4Algorithm,
		t.UTC().Format("20060102T150405Z"),
		v.scope(t),
		sha256Hex([]byte(creq)),
	}, "\n")

	sig := hex.EncodeToString(hmacSHA256(v.signingKey(t), sts))
	return fmt.Sprintf("%s Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		sigV4Algorithm, v.accessKey, v.scope(t), signedHeaders, sig)
}

// presign 生成预签名 URL（GET 语义；UNSIGNED-PAYLOAD）。
func (v sigV4) presign(method, rawURL string, ttl time.Duration, t time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("X-Amz-Algorithm", sigV4Algorithm)
	q.Set("X-Amz-Credential", fmt.Sprintf("%s/%s", v.accessKey, v.scope(t)))
	q.Set("X-Amz-Date", t.UTC().Format("20060102T150405Z"))
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(ttl.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = canonicalQuery(q)

	headers := map[string]string{"host": u.Host}
	signedHeaders, canonicalHeaders := canonicalizeHeaders(headers)
	creq := strings.Join([]string{
		method,
		canonicalURI(u.Path),
		canonicalQuery(u.Query()),
		canonicalHeaders,
		signedHeaders,
		"UNSIGNED-PAYLOAD",
	}, "\n")
	sts := strings.Join([]string{
		sigV4Algorithm,
		t.UTC().Format("20060102T150405Z"),
		v.scope(t),
		sha256Hex([]byte(creq)),
	}, "\n")
	sig := hex.EncodeToString(hmacSHA256(v.signingKey(t), sts))
	return u.String() + "&X-Amz-Signature=" + sig, nil
}

// canonicalizeHeaders 头小写化、按名排序、拼规范头块与署名头列表。
func canonicalizeHeaders(headers map[string]string) (signed, canonical string) {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		// 折叠连续空白（SigV4 规范）
		val := strings.Join(strings.Fields(headers[n]), " ")
		b.WriteString(val)
		b.WriteByte('\n')
	}
	return strings.Join(names, ";"), b.String()
}
