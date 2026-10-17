package collab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// The WebSocket upgrader had CheckOrigin return true for EVERY origin, under a comment
// claiming "production deployments tighten this via env" — no such env existed, so the claim
// was false and the policy was open everywhere. Because the edge gateway injects
// x-gateway-auth on requests it has already authenticated from a session cookie, a cross-site
// page could open a socket the gateway stamps with the victim's identity (CSWSH) and read the
// document stream.
func req(origin, host string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/v1/collab/p1/ws?client_id=c", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestOriginChecker_SameOriginByDefault(t *testing.T) {
	check := originChecker(nil)

	// The attack: a foreign site opening a socket to Docs.
	if check(req("https://evil.example", "docs.talyvor.com")) {
		t.Error("a foreign Origin must be REJECTED by default (CSWSH); the old policy returned true here")
	}
	// The legitimate case behind a reverse proxy: SPA and API share a hostname.
	if !check(req("https://docs.talyvor.com", "docs.talyvor.com")) {
		t.Error("same-origin must be allowed")
	}
	// Non-browser clients (Go test clients, CLI) send no Origin and cannot be cross-site.
	if !check(req("", "docs.talyvor.com")) {
		t.Error("a request with no Origin header must be allowed")
	}
	// Scheme differs but host matches — gorilla's own default compares host only, and behind
	// a TLS-terminating proxy the inbound scheme is http while the Origin says https.
	if !check(req("https://docs.talyvor.com", "docs.talyvor.com")) {
		t.Error("host-equal origin must be allowed regardless of scheme")
	}
	// A lookalike host must not pass.
	if check(req("https://docs.talyvor.com.evil.example", "docs.talyvor.com")) {
		t.Error("a suffix-lookalike host must be rejected")
	}
}

func TestOriginChecker_AllowListForSplitOrigin(t *testing.T) {
	// The dev setup the old comment was excusing: SPA on :5174, API on :4000.
	check := originChecker([]string{"http://localhost:5174", " http://127.0.0.1:5174/ "})

	if !check(req("http://localhost:5174", "localhost:4000")) {
		t.Error("a listed origin must be allowed")
	}
	if !check(req("http://127.0.0.1:5174", "localhost:4000")) {
		t.Error("entries must be trimmed and trailing-slash tolerant")
	}
	if check(req("https://evil.example", "localhost:4000")) {
		t.Error("an unlisted origin must be rejected even when an allow-list is set")
	}
	// A non-empty allow-list is exact: same-origin is NOT implicitly added, so an operator
	// who sets the variable gets exactly what they listed.
	if check(req("http://localhost:4000", "localhost:4000")) {
		t.Error("with an explicit allow-list, only listed origins pass")
	}
}

// CONTROL: the shape this replaced. If the assertions above can pass against an
// always-true checker, they are not testing anything.
func TestOriginChecker_OldPolicyWouldFail(t *testing.T) {
	alwaysTrue := func(_ *http.Request) bool { return true }
	if !alwaysTrue(req("https://evil.example", "docs.talyvor.com")) {
		t.Fatal("premise wrong: the old policy did not accept a foreign origin")
	}
	if originChecker(nil)(req("https://evil.example", "docs.talyvor.com")) {
		t.Error("the new policy must differ from the old one on exactly this input")
	}
}

// A Handler built without WithAllowedOrigins must be restrictive. The old package-level
// upgrader defaulted OPEN, so a caller that forgot to configure it got the permissive policy.
func TestNewHandler_DefaultsToSameOrigin(t *testing.T) {
	h := NewHandler(NewOTEngine())
	if h.upgrader.CheckOrigin == nil {
		t.Fatal("CheckOrigin must be installed, not left nil")
	}
	if h.upgrader.CheckOrigin(req("https://evil.example", "docs.talyvor.com")) {
		t.Error("an unconfigured Handler must still reject a foreign origin (fail-closed default)")
	}
}

type inScopeResolver struct{}

func (inScopeResolver) ResolveSession(context.Context, string) (bool, string, bool) {
	return true, "mbr_1", true
}

// TestServeWS_RefusesAForeignOriginUpgrade (B42.6) drives a real upgrade: with an in-scope
// caller, a foreign Origin is refused with 403 and no socket, while the app's own origin connects.
func TestServeWS_RefusesAForeignOriginUpgrade(t *testing.T) {
	h := NewHandler(NewOTEngine()).WithAccess(inScopeResolver{}).WithAllowedOrigins([]string{"https://docs.talyvor.com"})
	r := chi.NewRouter()
	r.Get("/v1/collab/{pageID}/ws", h.ServeWS)
	srv := httptest.NewServer(r)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/collab/p1/ws?client_id=c"

	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://evil.example"}})
	if err == nil {
		conn.Close()
		t.Fatal("a foreign Origin opened a collaboration socket")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Origin upgrade: resp=%v err=%v, want 403", resp, err)
	}

	conn, _, err = websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://docs.talyvor.com"}})
	if err != nil {
		t.Fatalf("the app's own origin was refused: %v", err)
	}
	conn.Close()
}
