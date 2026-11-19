package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/web"
)

func TestHealthz(t *testing.T) {
	r := web.NewRouter(false)
	web.MountHealth(r, web.HealthOptions{Version: "test-1.0"})

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: status = %d, want 200", rec.Code)
	}
	var env web.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("healthz: invalid envelope: %v", err)
	}
	if env.Code != 0 {
		t.Fatalf("healthz: code = %d, want 0", env.Code)
	}
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatal("healthz: data is not object")
	}
	if data["version"] != "test-1.0" {
		t.Fatalf("healthz: version = %v, want test-1.0", data["version"])
	}
	if data["uptime_s"] == nil {
		t.Fatal("healthz: missing uptime_s")
	}
	if data["goroutine"] == nil {
		t.Fatal("healthz: missing goroutine")
	}

	// readyz with no pingers → 全部 OK
	rec = httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz: status = %d, want 200", rec.Code)
	}
}

func TestReadyzFailingPinger(t *testing.T) {
	r := web.NewRouter(false)
	web.MountHealth(r, web.HealthOptions{
		Version: "test-1.0",
		Pingers: map[string]func(_ context.Context) error{
			"db": func(_ context.Context) error { return errors.New("connection refused") },
		},
	})

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz: status = %d, want 503", rec.Code)
	}
}

func TestRouterPathParams(t *testing.T) {
	r := web.NewRouter(false)
	var got string
	r.Handle(http.MethodGet, "/items/{id}/parts/{part}", func(_ http.ResponseWriter, req *http.Request) {
		got = req.PathValue("id") + "|" + req.PathValue("part")
	})

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/items/42/parts/b", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got != "42|b" {
		t.Fatalf("params = %q, want 42|b", got)
	}
}

func TestRouterGroup(t *testing.T) {
	r := web.NewRouter(false)
	g := r.Group("/api/v1")
	g.Handle(http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {
		web.OK(w, "pong")
	})

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
