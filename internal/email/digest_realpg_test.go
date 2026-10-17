package email_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/talyvor/docs/internal/email"
	"github.com/talyvor/docs/internal/freshness"
	"github.com/talyvor/docs/internal/membership"
	"github.com/talyvor/docs/internal/page"
	"github.com/talyvor/docs/internal/pagelink"
	"github.com/talyvor/docs/internal/testutil"
)

// B39.19: the 09:00 UTC digest mails an opted-in owner their stale pages and the approvals waiting for them, once,
// and mails nobody who has nothing or did not opt in.
func TestDailyDigestMailsOptedInOwnerTheirStalePagesAndWaitingApprovals_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	alice := member{"alice@corp.com", d.Member(t, ws, "alice@corp.com")} // opted in, two stale pages, one approval
	bob := member{"bob@corp.com", d.Member(t, ws, "bob@corp.com")}       // a stale page, not opted in
	carol := member{"carol@corp.com", d.Member(t, ws, "carol@corp.com")} // opted in, nothing
	dave := member{"dave@corp.com", d.Member(t, ws, "dave@corp.com")}    // asks alice to approve
	pref := func(m member, kind string, on bool) {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO notification_preferences (workspace_id, member_id, kind, email)
			VALUES ($1, $2, $3, $4)`, ws, m.id, kind, on); err != nil {
			t.Fatal(err)
		}
	}
	pref(alice, email.KindStaleDigest, true)
	pref(carol, email.KindStaleDigest, true)
	pref(alice, email.KindApprovalRequested, false) // so the only mail alice can get here is the digest
	stale := func(owner member, title string) {
		if _, err := d.Pool.Exec(ctx, `UPDATE pages SET stale_after_days = 30, updated_at = NOW() - INTERVAL '400 days'
			WHERE id = $1`, d.Page(t, ws, owner.id, title)); err != nil {
			t.Fatal(err)
		}
	}
	stale(alice, "Onboarding Guide")
	stale(alice, "Travel Policy")
	stale(bob, "Bob's Runbook")
	pageID := d.Page(t, ws, dave.id, "Pricing FAQ")
	var spaceID string
	if err := d.Pool.QueryRow(ctx, `SELECT space_id FROM pages WHERE id=$1`, pageID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	stub, got := smtpStub(t)
	sink := email.NewSink(d.Pool, stub)
	if rr := chain(d, ws, sink)(dave, "POST", "/v1/spaces/"+spaceID+"/pages/"+pageID+"/approval",
		`{"reviewers":["`+alice.id+`"]}`); rr.Code != http.StatusCreated {
		t.Fatalf("POST approval = %d %s", rr.Code, rr.Body.String())
	}

	eng := freshness.New(page.NewStore(d.Pool), pagelink.NewStore(d.Pool), nil).
		WithWorkspaces(membership.NewStore(d.Pool)).WithMailer(sink)
	for run := range 2 { // the second run, as on a second replica the same morning, queues nothing
		if _, err := eng.SendStaleDigestAll(ctx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if _, err := sink.SendDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	mails := got()
	if len(mails) != 1 {
		t.Fatalf("stub got %d mails, want exactly 1 (alice's digest): %q", len(mails), mails)
	}
	rcpt, data, _ := strings.Cut(mails[0], "\n")
	if rcpt != alice.email {
		t.Fatalf("digest went to %s, want %s", rcpt, alice.email)
	}
	for _, want := range []string{"Subject: Your Talyvor Docs digest: 2 stale pages, 1 approval waiting",
		`"Onboarding Guide"`, `"Travel Policy"`, `"Pricing FAQ"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("digest lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "Bob's Runbook") {
		t.Fatalf("alice's digest names bob's page:\n%s", data)
	}
}
