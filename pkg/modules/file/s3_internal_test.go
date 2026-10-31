package file

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------- SigV4：AWS 官方文档向量 ----------

// TestSigV4AWSDocumentVector AWS S3 SigV4 官方示例（GET Object +
// Range 头参与签名）：实现与标准逐字节对齐的锚点。
func TestSigV4AWSDocumentVector(t *testing.T) {
	v := sigV4{
		accessKey: "AKIAIOSFODNN7EXAMPLE",
		secretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		region:    "us-east-1",
		service:   "s3",
	}
	at, _ := time.Parse("20060102T150405Z", "20130524T000000Z")
	// 官方示例：GET /test.txt + Range 头；payload hash 为空串的
	// SHA-256（GET 无 body 的标准形态）。
	emptyHash := sha256Hex(nil)
	headers := map[string]string{
		"host":                 "examplebucket.s3.amazonaws.com",
		"range":                "bytes=0-9",
		"x-amz-content-sha256": emptyHash,
		"x-amz-date":           "20130524T000000Z",
	}
	got := v.sign(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt",
		headers, emptyHash, at)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got != want {
		t.Fatalf("Authorization =\n  %s\nwant\n  %s", got, want)
	}
}

// ---------- S3 兼容 stub（MinIO 协议子集 + 双向签名验证） ----------

// s3Stub 内存 S3：PUT/GET/DELETE/ListObjectsV2/预签名验签。
type s3Stub struct {
	t        *testing.T
	objects  map[string][]byte
	srv      *httptest.Server
	verifier sigV4
	// 记录
	puts, gets, deletes int
}

func newS3Stub(t *testing.T) *s3Stub {
	t.Helper()
	st := &s3Stub{t: t, objects: map[string][]byte{}}
	st.verifier = sigV4{accessKey: "testak", secretKey: "testsk", region: "us-east-1", service: "s3"}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/testbucket"), "/")
		if key == "" { // bucket 级操作（list）
			if r.URL.Query().Get("list-type") == "2" {
				var b strings.Builder
				b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
				for k, v := range st.objects {
					fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>%s</LastModified></Contents>`,
						k, len(v), "2026-10-31T00:00:00.000Z")
				}
				b.WriteString(`</ListBucketResult>`)
				w.Header().Set("Content-Type", "application/xml")
				io.WriteString(w, b.String())
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if !st.verifySig(r, body) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			st.objects[key] = body
			st.puts++
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			st.gets++
			if r.URL.Query().Get("X-Amz-Signature") != "" {
				// 预签名：query 验签
				if !st.verifyPresign(r) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			} else if !st.verifySig(r, nil) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			v, ok := st.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(v)
		case http.MethodDelete:
			st.deletes++
			if !st.verifySig(r, nil) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if _, ok := st.objects[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(st.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	st.srv = httptest.NewServer(mux)
	t.Cleanup(st.srv.Close)
	return st
}

// verifySig stub 侧用独立 sigV4 实例重算签名（双向锁实现正确性）。
func (st *s3Stub) verifySig(r *http.Request, body []byte) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return false
	}
	dateStr := r.Header.Get("x-amz-date")
	at, err := time.Parse("20060102T150405Z", dateStr)
	if err != nil {
		return false
	}
	headers := map[string]string{"host": r.Host}
	for _, h := range []string{"x-amz-date", "x-amz-content-sha256", "content-type"} {
		if v := r.Header.Get(h); v != "" {
			headers[h] = v
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	raw := scheme + "://" + r.Host + r.URL.RequestURI()
	want := st.verifier.sign(r.Method, raw, headers, sha256Hex(body), at)
	return auth == want
}

// verifyPresign 预签名 query 验签。
func (st *s3Stub) verifyPresign(r *http.Request) bool {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		return false
	}
	at, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil {
		return false
	}
	expires := q.Get("X-Amz-Expires")
	if expires == "" {
		return false
	}
	sig := q.Get("X-Amz-Signature")
	// 重建无签名的 URL 交由 presign 重算（同参数必须同签名）
	q.Del("X-Amz-Signature")
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	raw := scheme + "://" + r.Host + r.URL.Path + "?" + canonicalQuery(q)
	u, err := st.verifier.presign(http.MethodGet, raw, 15*time.Minute, at)
	if err != nil {
		return false
	}
	gotSig := strings.TrimPrefix(u[strings.LastIndex(u, "&"):], "&X-Amz-Signature=")
	return sig == gotSig
}

func newTestS3(t *testing.T, endpoint string) *S3Storage {
	t.Helper()
	s, err := NewS3Storage(endpoint, "us-east-1", "testbucket", "testak", "testsk", "", true)
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	return s
}

// TestS3StorageAllOps 全操作往返（stub 双向验签）：
// Save → Open 内容一致 → List → PresignURL 可直接下载 → Remove → 404。
func TestS3StorageAllOps(t *testing.T) {
	stub := newS3Stub(t)
	s3 := newTestS3(t, stub.srv.URL)
	ctx := context.Background()

	content := "S3 驱动测试内容 — hello 多云"
	n, err := s3.Save(ctx, "20261031/obj1.txt", strings.NewReader(content))
	if err != nil || n != int64(len(content)) {
		t.Fatalf("save: %d err=%v", n, err)
	}
	if stub.puts != 1 {
		t.Fatalf("puts = %d", stub.puts)
	}
	rc, err := s3.Open(ctx, "20261031/obj1.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != content {
		t.Fatalf("content = %q", string(b))
	}
	// List（prefix 过滤）
	objs, err := s3.ListObjects(ctx, "20261031")
	if err != nil || len(objs) != 1 || objs[0].Key != "20261031/obj1.txt" || objs[0].Size != int64(len(content)) {
		t.Fatalf("list = %+v err=%v", objs, err)
	}
	// 预签名 URL 直接 GET（stub query 验签）
	pu, err := s3.PresignURL(ctx, "20261031/obj1.txt", 15*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	resp, err := http.Get(pu)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("presign get: %v status=%d", err, resp.StatusCode)
	}
	pb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(pb) != content {
		t.Fatalf("presign content = %q", string(pb))
	}
	// Remove → Open 404（ErrNotFound 语义）
	if err := s3.Remove(ctx, "20261031/obj1.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s3.Open(ctx, "20261031/obj1.txt"); err != ErrNotFound {
		t.Fatalf("open after remove = %v, want ErrNotFound", err)
	}
	// Remove 幂等（404 视为已删）
	if err := s3.Remove(ctx, "20261031/obj1.txt"); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
}

// TestS3PrefixIsolation prefix 命名空间隔离：驱动侧前缀对上层透明。
func TestS3PrefixIsolation(t *testing.T) {
	stub := newS3Stub(t)
	s3, err := NewS3Storage(stub.srv.URL, "us-east-1", "testbucket", "testak", "testsk", "tenant-a", true)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	if _, err := s3.Save(ctx, "f.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 底层对象键带前缀（stub 侧直接观察）
	if _, ok := stub.objects["tenant-a/f.txt"]; !ok {
		t.Fatalf("objects = %v（前缀缺失）", stub.objects)
	}
	// 上层按相对键读
	rc, err := s3.Open(ctx, "f.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rc.Close()
}

// TestS3FromEnvErrors 凭据缺失三态：未配置（NotConfigured 哨兵）/
// endpoint 有但凭据缺（明细错误）/ 齐备成功。
func TestS3FromEnvErrors(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }

	if _, err := NewS3StorageFromEnv(get); err != ErrS3NotConfigured {
		t.Fatalf("empty env = %v, want ErrS3NotConfigured", err)
	}
	env["BASALT_FILE_S3_ENDPOINT"] = "http://127.0.0.1:9000"
	_, err := NewS3StorageFromEnv(get)
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("partial env err = %v（应明细报缺项）", err)
	}
	env["BASALT_FILE_S3_BUCKET"] = "b"
	env["BASALT_FILE_S3_AK"] = "ak"
	env["BASALT_FILE_S3_SK"] = "sk"
	s, err := NewS3StorageFromEnv(get)
	if err != nil || s.bucket != "b" {
		t.Fatalf("full env: %v", err)
	}
	if !s.pathStyle {
		t.Fatal("pathStyle 默认应 true")
	}
	env["BASALT_FILE_S3_PATH_STYLE"] = "false"
	if s2, _ := NewS3StorageFromEnv(get); s2.pathStyle {
		t.Fatal("path_style=false 未生效")
	}
}

// TestS3VirtualHostURL virtual-host 风格 URL 形态。
func TestS3VirtualHostURL(t *testing.T) {
	s, err := NewS3Storage("https://oss-cn-hangzhou.aliyuncs.com", "cn-hangzhou", "mybucket", "ak", "sk", "", false)
	if err != nil {
		t.Fatal(err)
	}
	u := s.objURL("dir/file.txt")
	if u != "https://mybucket.oss-cn-hangzhou.aliyuncs.com/dir/file.txt" {
		t.Fatalf("virtual url = %s", u)
	}
}

// TestS3SaveOverLimit 超限明确报错。
func TestS3SaveOverLimit(t *testing.T) {
	stub := newS3Stub(t)
	s3 := newTestS3(t, stub.srv.URL)
	big := make([]byte, MaxUploadSize+1)
	if _, err := s3.Save(context.Background(), "big.bin", strings.NewReader(string(big))); err == nil {
		t.Fatal("over-limit save 应拒绝")
	}
}

var _ = url.Values{} // 保持 import（canonicalQuery 在 stub 中使用）
