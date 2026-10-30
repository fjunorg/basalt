package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Notifier 是渠道适配器：完成一次实际发送（原子动作）。
// 实现方负责自有协议与鉴权；失败返回错误由模块统一记日志/审计。
type Notifier interface {
	Channel() string
	Send(ctx context.Context, to, subject, body string) error
}

// ---------- SMTP（标准库实现，零新依赖） ----------

// SMTPNotifier 经指定账号发送邮件。Password 经
// BASALT_NOTIFY_SMTP_PWD_<账号名大写> 注入（不落库）。
type SMTPNotifier struct {
	Cfg SMTPConfig
}

func (n *SMTPNotifier) Channel() string { return ChannelEmail }

func smtpPasswordOf(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return os.Getenv("BASALT_NOTIFY_SMTP_PWD_" + b.String())
}

func (n *SMTPNotifier) Send(_ context.Context, to, subject, body string) error {
	pwd := smtpPasswordOf(n.Cfg.Name)
	addr := fmt.Sprintf("%s:%d", n.Cfg.Host, n.Cfg.Port)
	auth := smtp.PlainAuth("", n.Cfg.Username, pwd, n.Cfg.Host)
	msg := strings.Join([]string{
		"From: " + n.Cfg.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	return smtp.SendMail(addr, auth, n.Cfg.From, []string{to}, []byte(msg))
}

// ---------- 阿里云短信（Dysmsapi RPC 签名，自研 HTTP 零依赖） ----------

// AliyunSMSNotifier 经阿里云短信 API 发送。AccessKey/Secret 经
// BASALT_NOTIFY_SMS_<渠道名大写>_KEY/_SECRET 注入（不落库）。
type AliyunSMSNotifier struct {
	Cfg     SMSChannel
	signer  AliyunSigner
	baseURL string // 测试可注入 stub URL
}

// AliyunSigner 抽象 RPC 签名（测试可替换）。
type AliyunSigner func(method, paramStr string) string

func smsEnvOf(name, suffix string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return os.Getenv("BASALT_NOTIFY_SMS_" + b.String() + "_" + suffix)
}

func (n *AliyunSMSNotifier) Channel() string { return ChannelSMS }

// NewAliyunSMSNotifier 构造生产适配器（公网端点 + HMAC-SHA1 签名）。
func NewAliyunSMSNotifier(cfg SMSChannel) *AliyunSMSNotifier {
	region := cfg.Region
	if region == "" {
		region = "cn-hangzhou"
	}
	return &AliyunSMSNotifier{
		Cfg:     cfg,
		baseURL: "https://dysmsapi." + region + ".aliyuncs.com/",
		signer:  aliyunSignHMACSHA1(smsEnvOf(cfg.Name, "SECRET")),
	}
}

// WithBaseURL 替换 API 端点（测试注入 stub 用）。
func WithBaseURL(n *AliyunSMSNotifier, url string) *AliyunSMSNotifier {
	n.baseURL = url
	return n
}

// aliyunSignHMACSHA1 生成 RPC V1 签名（按参数排序拼接 + HMAC-SHA1）。
func aliyunSignHMACSHA1(secret string) AliyunSigner {
	return func(method, paramStr string) string {
		mac := hmac.New(sha1.New, []byte(secret+"&"))
		mac.Write([]byte(method + "&" + url.QueryEscape("/") + "&" + percentEncode(paramStr)))
		return base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
}

// percentEncode 阿里云 RPC 规范的二次编码。
func percentEncode(s string) string {
	return url.QueryEscape(url.QueryEscape(s))
}

// Send 发送一条短信。短信渠道参数语义：subject 位承载平台模板
// Code（阿里云 TemplateCode），body 位承载渲染后的 TemplateParam
// JSON（notify 模板的 Content 即 JSON 变量模板，如 {"code":"{{code}}"}）。
func (n *AliyunSMSNotifier) Send(ctx context.Context, to, templateCode, templateParam string) error {
	key := smsEnvOf(n.Cfg.Name, "KEY")
	secret := smsEnvOf(n.Cfg.Name, "SECRET")
	if key == "" || secret == "" {
		return fmt.Errorf("aliyun sms credentials not configured (env BASALT_NOTIFY_SMS_%s_KEY/_SECRET)", strings.ToUpper(n.Cfg.Name))
	}
	params := map[string]string{
		"Action":           "SendSms",
		"Version":          "2017-05-25",
		"AccessKeyId":      key,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"SignatureNonce":   newID(),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"PhoneNumbers":     to,
		"SignName":         n.Cfg.Sign,
		"TemplateCode":     templateCode,
		"TemplateParam":    templateParam,
		"Format":           "JSON",
	}
	// 签名：按 key 排序的 & 连接串
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var pb strings.Builder
	for i, k := range keys {
		if i > 0 {
			pb.WriteString("&")
		}
		pb.WriteString(percentEncode(k) + "=" + percentEncode(params[k]))
	}
	paramStr := pb.String()
	signer := n.signer
	if signer == nil {
		signer = aliyunSignHMACSHA1(secret)
	}
	reqURL := n.baseURL + "?Signature=" + url.QueryEscape(signer("GET", paramStr)) + "&" + paramStr

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("aliyun sms decode: %v: %s", err, string(raw))
	}
	if out.Code != "OK" {
		return fmt.Errorf("aliyun sms %s: %s", out.Code, out.Message)
	}
	return nil
}

// ---------- 腾讯云短信（接口缝：凭据与签名体系不同，待真机验证实装） ----------

// TencentSMSNotifier 保留接口缝：构造返回适配器但 Send 明确报
// ErrBadProvider——实装需 TC3-HMAC-SHA256 签名与真机验证，随
// 信创/云真机矩阵落地（与 postgres 驱动同批）。
type TencentSMSNotifier struct{ Cfg SMSChannel }

func (n *TencentSMSNotifier) Channel() string { return ChannelSMS }

func (n *TencentSMSNotifier) Send(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("%w: tencent sms adapter pending (wiring with cloud matrix)", ErrBadProvider)
}
