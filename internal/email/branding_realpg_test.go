package email_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/talyvor/docs/internal/authz"
	"github.com/talyvor/docs/internal/email"
	"github.com/talyvor/docs/internal/testutil"
)

// B39.20: an owner brands the workspace's mail; a branded workspace's mail carries its name, logo and colour, an
// unbranded one Talyvor's default, and a bad colour or a non-https logo is refused.
func TestBrandedWorkspaceMailShowsItsNameLogoAndColour_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	acme, plain := d.Workspace(t), d.Workspace(t)
	call := func(role, method, body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
				next.ServeHTTP(w, rq.WithContext(authz.WithMemberships(rq.Context(), role+"@acme.com",
					[]authz.Membership{{WorkspaceID: acme, MemberID: role, Role: role}})))
			})
		})
		r.Route("/v1", email.NewBrandingHandler(d.Pool).Mount)
		rq := httptest.NewRequest(method, "/v1/workspaces/"+acme+"/email-branding", strings.NewReader(body))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, rq)
		return rr
	}

	for _, bad := range []string{`{"header_color":"red"}`, `{"header_color":"#12345"}`,
		`{"logo_url":"http://acme.example/logo.png"}`, `{"logo_url":"javascript:alert(1)"}`} {
		if rr := call("owner", "PUT", bad); rr.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d %s, want 400", bad, rr.Code, rr.Body.String())
		}
	}
	brand := `{"from_name":"Acme","header_color":"#1E40AF","logo_url":"https://acme.example/logo.png"}`
	if rr := call("member", "PUT", brand); rr.Code != http.StatusForbidden {
		t.Fatalf("member PUT = %d, want 403", rr.Code)
	}
	if rr := call("owner", "PUT", brand); rr.Code != http.StatusOK {
		t.Fatalf("owner PUT = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call("member", "GET", ""); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"header_color":"#1E40AF"`) {
		t.Fatalf("member GET = %d %s", rr.Code, rr.Body.String())
	}

	for _, ws := range []string{acme, plain} {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO notification_outbox (workspace_id, member_id, kind, to_email, subject, body)
			VALUES ($1, 'm', 'approval.requested', $2, 'Your approval is requested: Runbook', 'Dana asked you to approve "Runbook".')`,
			ws, ws+"@corp.com"); err != nil {
			t.Fatal(err)
		}
	}
	stub, got := smtpStub(t)
	if _, err := email.NewSink(d.Pool, stub).WithDefaultLogo("https://docs.example/email-logo.png").SendDue(ctx); err != nil {
		t.Fatal(err)
	}
	mailTo := func(ws string) string {
		for _, m := range got() {
			if strings.HasPrefix(m, ws+"@corp.com\n") {
				return m
			}
		}
		t.Fatalf("no mail for %s in %q", ws, got())
		return ""
	}
	branded := mailTo(acme)
	for _, want := range []string{`From: "Acme via Talyvor" <docs@talyvor.com>`, "Content-Type: text/html",
		`<img src="https://acme.example/logo.png" alt="Acme"`, "background:#1E40AF", "asked you to approve"} {
		if !strings.Contains(branded, want) {
			t.Fatalf("branded mail lacks %q:\n%s", want, branded)
		}
	}
	unbranded := mailTo(plain)
	for _, want := range []string{"From: Talyvor Docs <docs@talyvor.com>", "background:#060A12",
		`<img src="https://docs.example/email-logo.png" alt="Talyvor"`} {
		if !strings.Contains(unbranded, want) {
			t.Fatalf("unbranded mail lacks %q:\n%s", want, unbranded)
		}
	}
	if strings.Contains(unbranded, "via Talyvor") {
		t.Fatalf("unbranded mail carries a workspace From name:\n%s", unbranded)
	}
}
