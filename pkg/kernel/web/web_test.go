package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/web"
)

func TestHealthz(t *testing.T) {
	r := web.NewRouter(false)
	web.MountHealth(r)

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		var env web.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: invalid envelope: %v", path, err)
		}
		if env.Code != 0 {
			t.Fatalf("%s: code = %d, want 0", path, env.Code)
		}
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
