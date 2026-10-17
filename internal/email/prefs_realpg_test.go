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
	"github.com/talyvor/docs/internal/freshness"
	"github.com/talyvor/docs/internal/membership"
	"github.com/talyvor/docs/internal/page"
	"github.com/talyvor/docs/internal/pagelink"
	"github.com/talyvor/docs/internal/testutil"
)

// B39.32: a member turns the digest on from Settings and the next run mails them; nobody can set another member's
// choice; an unknown kind is refused.
func TestMemberTurnsDigestOnAndNextRunMailsThem_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws, other := d.Workspace(t), d.Workspace(t)
	alice := member{"alice@corp.com", d.Member(t, ws, "alice@corp.com")}
	bob := member{"bob@corp.com", d.Member(t, ws, "bob@corp.com")}
	call := func(m member, inWS, method, body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
				next.ServeHTTP(w, rq.WithContext(authz.WithMemberships(rq.Context(), m.email,
					[]authz.Membership{{WorkspaceID: ws, MemberID: m.id, Role: "member"}})))
			})
		})
		r.Route("/v1", email.NewPrefsHandler(d.Pool).Mount)
		rq := httptest.NewRequest(method, "/v1/workspaces/"+inWS+"/notification-preferences", strings.NewReader(body))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, rq)
		return rr
	}

	if rr := call(alice, ws, "GET", ""); rr.Code != 200 ||
		!strings.Contains(rr.Body.String(), `"freshness.digest":false`) || !strings.Contains(rr.Body.String(), `"approval.requested":true`) {
		t.Fatalf("GET defaults = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(alice, ws, "PUT", `{"freshness.digest":true}`); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"freshness.digest":true`) {
		t.Fatalf("PUT on = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(alice, ws, "PUT", `{"weekly.spam":true}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d, want 400", rr.Code)
	}
	// bob cannot name alice, and a workspace he is not in is refused.
	if rr := call(bob, ws, "PUT", `{"member_id":"`+alice.id+`","freshness.digest":false}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT naming another member = %d, want 400", rr.Code)
	}
	if rr := call(bob, other, "PUT", `{"freshness.digest":false}`); rr.Code != http.StatusForbidden {
		t.Fatalf("PUT in a foreign workspace = %d, want 403", rr.Code)
	}
	if rr := call(bob, ws, "PUT", `{"freshness.digest":false}`); rr.Code != 200 {
		t.Fatalf("bob's own PUT = %d", rr.Code)
	}

	for _, owner := range []member{alice, bob} {
		if _, err := d.Pool.Exec(ctx, `UPDATE pages SET stale_after_days = 30, updated_at = NOW() - INTERVAL '400 days'
			WHERE id = $1`, d.Page(t, ws, owner.id, owner.email+" runbook")); err != nil {
			t.Fatal(err)
		}
	}
	stub, got := smtpStub(t)
	sink := email.NewSink(d.Pool, stub)
	eng := freshness.New(page.NewStore(d.Pool), pagelink.NewStore(d.Pool), nil).
		WithWorkspaces(membership.NewStore(d.Pool)).WithMailer(sink)
	if _, err := eng.SendStaleDigestAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.SendDue(ctx); err != nil {
		t.Fatal(err)
	}
	mails := got()
	if len(mails) != 1 || !strings.HasPrefix(mails[0], alice.email+"\n") || !strings.Contains(mails[0], "alice@corp.com runbook") {
		t.Fatalf("want exactly alice's digest, got %q", mails)
	}
}
