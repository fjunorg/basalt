package file

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Storage 是 S3 兼容对象存储驱动（自研 SigV4，零 SDK 依赖）。
//
// 兼容矩阵：MinIO / 阿里云 OSS（S3 兼容模式）/ 腾讯云 COS / 任意
// S3 兼容网关；pathStyle=true 时 URL 形如 endpoint/bucket/key
// （MinIO 与多数自建网关），virtual-host 风格（bucket.endpoint/key，
// OSS/COS 默认）置 false。非 S3 协议云（七牛原生 API 等）经 Storage
// 接口缝后续单独接入，不在此驱动内做多协议分支。
//
// 租户隔离边界：prefix 支持命名空间前缀（建议 {tenantID}/，见
// docs/file-s3.md §3）；bucket 级隔离（每租户独立 bucket）由部署
// 侧配合 BASALT_FILE_S3_BUCKET 动态化实现，驱动不感知。
type S3Storage struct {
	endpoint  string // https://s3.example.com（不含 bucket）
	bucket    string
	region    string
	ak, sk    string
	pathStyle bool
	prefix    string // 对象键前缀（命名空间隔离）
	client    *http.Client
}

// NewS3Storage 显式构造。
func NewS3Storage(endpoint, region, bucket, ak, sk, prefix string, pathStyle bool) (*S3Storage, error) {
	if endpoint == "" || bucket == "" || ak == "" || sk == "" {
		return nil, fmt.Errorf("file: S3 storage requires endpoint/bucket/access-key/secret-key (BASALT_FILE_S3_*)")
	}
	if region == "" {
		region = "us-east-1"
	}
	return &S3Storage{
		endpoint: strings.TrimRight(endpoint, "/"), bucket: bucket,
		region: region, ak: ak, sk: sk,
		pathStyle: pathStyle, prefix: strings.Trim(prefix, "/"),
		client: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// ErrS3NotConfigured S3 凭据缺失的哨兵（明确报错而非静默回落）。
var ErrS3NotConfigured = fmt.Errorf("file: S3 storage not configured")

// NewS3StorageFromEnv 经 BASALT_FILE_S3_ENDPOINT/REGION/BUCKET/AK/SK/
// PREFIX/PATH_STYLE 构造。ENDPOINT 缺失返回 ErrS3NotConfigured
// （调用方以此判定"未启用 S3"）；ENDPOINT 有但凭据不全报明细错误。
func NewS3StorageFromEnv(getenv func(string) string) (*S3Storage, error) {
	if getenv("BASALT_FILE_S3_ENDPOINT") == "" {
		return nil, ErrS3NotConfigured
	}
	pathStyle := true // MinIO/自建网关默认 path-style
	if getenv("BASALT_FILE_S3_PATH_STYLE") == "false" {
		pathStyle = false // OSS/COS virtual-host 风格
	}
	return NewS3Storage(
		getenv("BASALT_FILE_S3_ENDPOINT"),
		getenv("BASALT_FILE_S3_REGION"),
		getenv("BASALT_FILE_S3_BUCKET"),
		getenv("BASALT_FILE_S3_AK"),
		getenv("BASALT_FILE_S3_SK"),
		getenv("BASALT_FILE_S3_PREFIX"),
		pathStyle,
	)
}

// objURL 对象请求 URL（含 bucket 挂载风格）。
func (s *S3Storage) objURL(key string) string {
	k := s.prefix + "/" + strings.TrimPrefix(key, "/")
	var u string
	if s.pathStyle {
		u = fmt.Sprintf("%s/%s%s", s.endpoint, s.bucket, canonicalURI(k))
	} else {
		host := s.hostOf()
		u = fmt.Sprintf("%s%s", host, canonicalURI(k))
	}
	return u
}

// hostOf virtual-host 风格的 scheme://bucket.host 前缀。
func (s *S3Storage) hostOf() string {
	u, _ := url.Parse(s.endpoint)
	return fmt.Sprintf("%s://%s.%s", u.Scheme, s.bucket, u.Host)
}

// keyOf 从响应对象键还原相对键（去前缀）。
func (s *S3Storage) keyOf(objKey string) string {
	if s.prefix != "" {
		return strings.TrimPrefix(strings.TrimPrefix(objKey, s.prefix), "/")
	}
	return objKey
}

// do 签名并发起请求（body 为 nil 时无 payload）。
func (s *S3Storage) do(ctx context.Context, method, rawURL string, body []byte, contentType string, extraQuery url.Values) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if len(extraQuery) > 0 {
		q := u.Query()
		for k, vs := range extraQuery {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = canonicalQuery(q)
	}
	var rd io.Reader
	payloadHash := sha256Hex(nil)
	if body != nil {
		rd = bytes.NewReader(body)
		payloadHash = sha256Hex(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	req.Header.Set("x-amz-date", now.Format("20060102T150405Z"))
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	headers := map[string]string{"host": req.URL.Host}
	for _, h := range []string{"x-amz-date", "x-amz-content-sha256", "content-type"} {
		if v := req.Header.Get(h); v != "" {
			headers[h] = v
		}
	}
	sig := (sigV4{accessKey: s.ak, secretKey: s.sk, region: s.region, service: "s3"}).
		sign(method, u.String(), headers, payloadHash, now)
	req.Header.Set("Authorization", sig)
	return s.client.Do(req)
}

// Save PUT 对象。上游 32MB 上限（MaxUploadSize）内整体缓冲后签名
// 上传（payload 参与签名，避免分块签名复杂度）。
func (s *S3Storage) Save(ctx context.Context, rel string, r io.Reader) (int64, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxUploadSize+1))
	if err != nil {
		return 0, err
	}
	if int64(len(body)) > MaxUploadSize {
		return 0, fmt.Errorf("file: object exceeds %d bytes", MaxUploadSize)
	}
	resp, err := s.do(ctx, http.MethodPut, s.objURL(rel), body, "application/octet-stream", nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return 0, fmt.Errorf("file: S3 put %s: status %d: %s", rel, resp.StatusCode, string(b))
	}
	return int64(len(body)), nil
}

// Open GET 对象。
func (s *S3Storage) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, s.objURL(rel), nil, "", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		return nil, fmt.Errorf("file: S3 get %s: status %d: %s", rel, resp.StatusCode, string(b))
	}
	return resp.Body, nil
}

// Remove DELETE 对象（幂等语义：404 视为已删）。
func (s *S3Storage) Remove(ctx context.Context, rel string) error {
	resp, err := s.do(ctx, http.MethodDelete, s.objURL(rel), nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("file: S3 delete %s: status %d", rel, resp.StatusCode)
}

// ---------- 扩展能力（Storage 接口缝之上的可选实现） ----------

// ObjectInfo 列表条目。
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// ListObjectsV2 列出前缀下对象（单页 ≤1000，返回相对键）。
func (s *S3Storage) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	listURL := s.listBaseURL()
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", s.prefix+"/"+strings.TrimPrefix(prefix, "/"))
	resp, err := s.do(ctx, http.MethodGet, listURL, nil, "", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("file: S3 list: status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	objs := make([]ObjectInfo, 0, len(out.Contents))
	for _, c := range out.Contents {
		lm, _ := time.Parse(time.RFC3339, c.LastModified)
		objs = append(objs, ObjectInfo{Key: s.keyOf(c.Key), Size: c.Size, LastModified: lm})
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	return objs, nil
}

func (s *S3Storage) listBaseURL() string {
	if s.pathStyle {
		return fmt.Sprintf("%s/%s", s.endpoint, s.bucket)
	}
	return s.hostOf()
}

// PresignURL 预签名 GET URL（客户端直连对象存储下载，服务端零流量）。
// TTL 建议 ≤1h（与访问令牌生命周期对齐）。
func (s *S3Storage) PresignURL(ctx context.Context, rel string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return "", fmt.Errorf("file: presign ttl must be in (0, 24h]")
	}
	return (sigV4{accessKey: s.ak, secretKey: s.sk, region: s.region, service: "s3"}).
		presign(http.MethodGet, s.objURL(rel), ttl, time.Now().UTC())
}
