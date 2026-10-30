package audit

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// ---------- 链尾签名 HTTP 端点 ----------

// signHead 对当前链尾签名（audit:sign）。
func (m *Module) signHead(w http.ResponseWriter, r *http.Request) {
	sc := m.signedChain()
	if sc == nil || m.chainSigner == nil {
		web.Err(w, http.StatusServiceUnavailable,
			"chain signing not available (requires persistent chain and BASALT_SM2_CHAIN_SIGN=on)")
		return
	}
	rec, err := sc.SignHead(r.Context(), m.chainSigner)
	switch {
	case errors.Is(err, ErrEmptyChain):
		web.Err(w, http.StatusConflict, "audit chain is empty (nothing to sign)")
		return
	case err != nil:
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.Created(w, rec)
}

// listHeadSigs 列出签名记录（audit:read，倒序）。
func (m *Module) listHeadSigs(w http.ResponseWriter, r *http.Request) {
	sc := m.signedChain()
	if sc == nil {
		web.Err(w, http.StatusServiceUnavailable, "chain signing not available (persistent chain required)")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	recs, err := sc.ListHeadSignatures(r.Context(), limit)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	web.OK(w, map[string]any{"count": len(recs), "items": recs})
}

// verifyHeadSigs 核验签名记录（audit:read）：SigOK 防伪造签名、
// ChainOK 防删改/重算历史（防抵赖双向核验）。
func (m *Module) verifyHeadSigs(w http.ResponseWriter, r *http.Request) {
	sc := m.signedChain()
	if sc == nil || m.chainSigner == nil {
		web.Err(w, http.StatusServiceUnavailable,
			"chain signature verification not available (requires persistent chain and BASALT_SM2_CHAIN_SIGN=on)")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	checks, err := sc.VerifyHeadSignatures(r.Context(), m.chainSigner, limit)
	if err != nil {
		web.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	allOK := true
	for _, c := range checks {
		if !c.SigOK || !c.ChainOK {
			allOK = false
			break
		}
	}
	web.OK(w, map[string]any{
		"all_ok": allOK,
		"count":  len(checks),
		"checks": checks,
	})
}
