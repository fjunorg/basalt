// Package web 提供内核 HTTP 能力：路由抽象（gin 底座）、健康检查、统一响应。
//
// 模块代码只依赖本包类型与标准库，不直接 import gin。
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
)

// Envelope 是全部 API 的统一响应外壳。
// G 端集成商依赖稳定的 code/message 结构做错误分支。
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func WriteJSON(w http.ResponseWriter, status int, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Envelope{Code: code, Message: msg, Data: data})
}

func OK(w http.ResponseWriter, data any)      { WriteJSON(w, http.StatusOK, 0, "ok", data) }
func Created(w http.ResponseWriter, data any) { WriteJSON(w, http.StatusCreated, 0, "created", data) }
func Err(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, status, msg, nil)
}

// HealthOptions 配置内核健康探针端点的输出。全部可选：
//   Version    — 由 ldflags -X main.version 注入的版本标识
//   EmbedHash  — ldflags -X main.embedHash 注入的 console/dist 产物总 hash
//   StartTime  — 进程启动时间（用于 uptime 展示）
//   Pingers    — readiness 检查集合：键=名称，值=ping 函数返回 nil 表示通过
type HealthOptions struct {
	Version   string
	EmbedHash string
	StartTime time.Time
	Pingers   map[string]func(ctx context.Context) error
}

// MountHealth 挂载内核级探针端点。
//
// /healthz  —— K8s 存活探针。永不依赖外部资源（DB/网络等），只返回
//   进程自身状态：version、uptime、goroutine 数、内存分配。必须始终 200。
// /readyz   —— K8s 就绪探针。聚合 opts.Pingers：任何一项失败即返回 503
//   + 失败项明细。无 pinger 时（纯内存演示模式）等同 healthz。
func MountHealth(r Router, opts HealthOptions) {
	start := opts.StartTime
	if start.IsZero() {
		start = time.Now()
	}

	version := opts.Version
	if version == "" {
		// 回退：尝试从 runtime/debug 读取 VCS 修订（go build -buildvcs=true 时注入）
		if bi, ok := debug.ReadBuildInfo(); ok {
			version = bi.Main.Version
			if version == "(devel)" || version == "" {
				for _, s := range bi.Settings {
					if s.Key == "vcs.revision" && s.Value != "" {
						version = s.Value[:8]
						break
					}
				}
			}
		}
	}
	if version == "" {
		version = "dev"
	}

	r.Handle(http.MethodGet, "/healthz", func(w http.ResponseWriter, _ *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		out := map[string]any{
			"status":    "up",
			"version":   version,
			"uptime_s":  int(time.Since(start).Seconds()),
			"goroutine": runtime.NumGoroutine(),
			"alloc_mb":  roundMB(m.Alloc),
			"sys_mb":    roundMB(m.Sys),
		}
		if opts.EmbedHash != "" {
			out["embed_hash"] = opts.EmbedHash
		}
		OK(w, out)
	})

	r.Handle(http.MethodGet, "/readyz", func(w http.ResponseWriter, _ *http.Request) {
		results := map[string]string{}
		allOK := true
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for name, ping := range opts.Pingers {
			if err := ping(ctx); err != nil {
				results[name] = "FAIL: " + err.Error()
				allOK = false
			} else {
				results[name] = "OK"
			}
		}
		// 始终附带基本元信息（便于排查 ready 失败原因）
		out := map[string]any{
			"status":  map[bool]string{true: "ready", false: "not_ready"}[allOK],
			"version": version,
			"checks":  results,
		}
		if allOK {
			OK(w, out)
		} else {
			WriteJSON(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "one or more checks failed", out)
		}
	})
}

func roundMB(bytes uint64) float64 {
	return float64(bytes) / float64(1<<20)
}
