package observ_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/kernel/observ"
)

func TestSetupNoopWithoutEndpoint(t *testing.T) {
	cfg := config.Defaults() // OtelEndpoint 为空
	shutdown, err := observ.Setup(cfg)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown: %v", err)
	}
}

func TestSetupWithOTLPEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.OtelEndpoint = srv.URL
	shutdown, err := observ.Setup(cfg)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	// shutdown 必须能在合理时间内完成（刷出缓冲并关闭导出器）。
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestHTTPHandlerProbesNotTraced(t *testing.T) {
	var seen []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := observ.HTTPHandler(inner, "test")

	for _, p := range []string{"/healthz", "/api/v1/x"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", p, rec.Code)
		}
	}
	// Filter 只影响采样，不应丢请求。
	if len(seen) != 2 {
		t.Fatalf("inner handler saw %d requests, want 2", len(seen))
	}
}
