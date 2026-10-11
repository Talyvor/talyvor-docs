package gatewayauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ⚠ AN UNSET SECRET MUST REFUSE EVERYTHING. sha256("") is the digest of an ABSENT x-gateway-auth
// header as well as of an empty configured secret, so without the explicit guard the constant-time
// compare MATCHES and an unauthenticated caller crosses the root-of-trust boundary — every /v1
// route, not one. config.Load refuses to boot below MinGatewayAuthSecretLen, but this boundary must
// not depend on a check that lives somewhere else.
func TestMiddleware_UnsetSecretRefusesEveryRequest(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Middleware("", nil)(next)

	for _, tc := range []struct{ name, proof string }{
		{"no proof header at all", ""},
		{"empty proof header", ""},
		{"any proof header", "anything"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/spaces", nil)
			if tc.proof != "" {
				req.Header.Set(HeaderGatewayAuth, tc.proof)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unset secret + %s = %d, want 401 — an empty secret must fail CLOSED",
					tc.name, rec.Code)
			}
		})
	}
}

// B17.211: the gateway names the caller's Lens workspace, and it is read only once the proof holds
// — it decides whose money a Lens call moves.
func TestMiddleware_LiftsTheCallersLensWorkspaceBehindTheProof(t *testing.T) {
	var seen Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := Middleware("the-secret", nil)(next)
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws-1/ai/ask", nil)
	req.Header.Set(HeaderGatewayAuth, "the-secret")
	req.Header.Set(HeaderUserEmail, "a@x.com")
	req.Header.Set(HeaderWorkspace, "u-lens")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen.LensWorkspace != "u-lens" {
		t.Fatalf("status %d, identity %+v — want 200 and LensWorkspace u-lens", rec.Code, seen)
	}
}
