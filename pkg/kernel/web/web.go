// Package web 提供内核 HTTP 能力：路由抽象（gin 底座）、健康检查、统一响应。
//
// 模块代码只依赖本包类型与标准库，不直接 import gin。
package web

import (
	"encoding/json"
	"net/http"
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

// MountHealth 挂载内核级探针端点。
// /healthz 供负载均衡存活探测（永不依赖模块状态）；
// /readyz 供发布前就绪检查，M2 起聚合各模块 HealthCheck。
func MountHealth(r Router) {
	r.Handle(http.MethodGet, "/healthz", func(w http.ResponseWriter, _ *http.Request) {
		OK(w, map[string]string{"status": "up"})
	})
	r.Handle(http.MethodGet, "/readyz", func(w http.ResponseWriter, _ *http.Request) {
		OK(w, map[string]string{"status": "ready"})
	})
}
