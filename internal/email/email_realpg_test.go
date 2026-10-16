package email_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/docs/internal/approval"
	"github.com/talyvor/docs/internal/authz"
	"github.com/talyvor/docs/internal/comment"
	"github.com/talyvor/docs/internal/email"
	"github.com/talyvor/docs/internal/page"
	"github.com/talyvor/docs/internal/permission"
	"github.com/talyvor/docs/internal/space"
	"github.com/talyvor/docs/internal/testutil"
)

// smtpStub is a mail server that accepts everything and keeps each message's recipient and data.
func smtpStub(t *testing.T) (email.SMTP, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				say := func(s string) { _, _ = conn.Write([]byte(s)) }
				say("220 stub\r\n")
				var rcpt string
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "RCPT TO:"):
						rcpt = strings.Trim(strings.TrimSpace(line)[8:], "<>")
						say("250 ok\r\n")
					case cmd == "DATA":
						say("354 go\r\n")
						var data strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
							data.WriteString(l)
						}
						mu.Lock()
						got = append(got, rcpt+"\n"+data.String())
						mu.Unlock()
						say("250 queued\r\n")
					case cmd == "QUIT":
						say("221 bye\r\n")
						return
					default:
						say("250 ok\r\n")
					}
				}
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	return email.SMTP{Host: "127.0.0.1", Port: port, From: "Talyvor Docs <docs@talyvor.com>"},
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

type member struct{ email, id string }

// chain mounts the approval and comment handlers with cmd/docs/main.go's page enforcer, behind a boundary that
// installs the caller's verified membership, both notifying sink.
func chain(d *testutil.DB, wsID string, sink *email.Sink) func(m member, method, path, body string) *httptest.ResponseRecorder {
	permStore := permission.NewStore(d.Pool)
	pageStore, spaceStore := page.NewStore(d.Pool), space.NewStore(d.Pool)
	looker := func(ctx context.Context, id string) (permission.PageMeta, error) {
		p, err := pageStore.GetByIDInWorkspaces(ctx, id, authz.WorkspaceIDs(ctx))
		if err != nil {
			return permission.PageMeta{}, err
		}
		sp, err := spaceStore.GetByIDInWorkspaces(ctx, p.SpaceID, authz.WorkspaceIDs(ctx))
		if err != nil {
			return permission.PageMeta{}, err
		}
		return permission.PageMeta{WorkspaceID: p.WorkspaceID, SpaceID: p.SpaceID,
			SpaceCreatedBy: sp.CreatedBy, SpacePrivate: sp.Private, PageCreatedBy: p.CreatedBy}, nil
	}
	pageEnf := permission.NewEnforcer(permStore, permission.PageResolverFromParam("pageID", looker, permStore))
	return func(m member, method, path, body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
				c := authz.WithMemberships(rq.Context(), m.email,
					[]authz.Membership{{WorkspaceID: wsID, MemberID: m.id, Role: "member"}})
				next.ServeHTTP(w, rq.WithContext(c))
			})
		})
		r.Route("/v1", func(r chi.Router) {
			approval.NewHandler(approval.NewStore(d.Pool)).WithAccess(pageEnf).WithNotifier(sink).Mount(r)
			comment.NewHandler(comment.NewStore(d.Pool)).WithAccess(pageEnf).WithNotifier(sink).Mount(r)
		})
		rq := httptest.NewRequest(method, path, strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, rq)
		return rr
	}
}

func TestApprovalRequestMailsEachReviewerOnceAcrossARestart_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	alice := member{"alice@corp.com", d.Member(t, ws, "alice@corp.com")}
	carol := member{"carol@corp.com", d.Member(t, ws, "carol@corp.com")}
	dave := member{"dave@corp.com", d.Member(t, ws, "dave@corp.com")}
	pageID := d.Page(t, ws, alice.id, "Expenses Policy")
	var spaceID string
	if err := d.Pool.QueryRow(ctx, `SELECT space_id FROM pages WHERE id=$1`, pageID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	stub, got := smtpStub(t)
	call := chain(d, ws, email.NewSink(d.Pool, stub)) // this process stops before its worker runs
	base := "/v1/spaces/" + spaceID + "/pages/" + pageID

	if rr := call(alice, "POST", base+"/approval",
		`{"reviewers":["`+carol.id+`","`+dave.id+`"],"message":"please check the limits"}`); rr.Code != http.StatusCreated {
		t.Fatalf("POST approval = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(alice, "POST", base+"/comments", `{"content":"hi"}`); rr.Code != http.StatusCreated {
		t.Fatalf("POST comment = %d %s", rr.Code, rr.Body.String())
	}

	restarted, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { email.NewSink(d.Pool, stub).Start(restarted, time.Hour); close(done) }()
	waitFor(t, func() bool { return len(got()) >= 2 })
	cancel()
	<-done
	for range 2 { // later runs, as after another restart, must find nothing left to send
		if n, err := email.NewSink(d.Pool, stub).SendDue(ctx); err != nil || n != 0 {
			t.Fatalf("a later run found %d to send (%v)", n, err)
		}
	}

	mails := got()
	if len(mails) != 2 {
		t.Fatalf("stub got %d mails, want exactly 2 (one per reviewer, none for the comment): %q", len(mails), mails)
	}
	to := map[string]string{}
	for _, m := range mails {
		rcpt, data, _ := strings.Cut(m, "\n")
		to[rcpt] = data
	}
	for _, r := range []member{carol, dave} {
		data, ok := to[r.email]
		if !ok || !strings.Contains(data, `alice@corp.com asked you to approve "Expenses Policy"`) ||
			!strings.Contains(data, "please check the limits") || !strings.Contains(data, "Subject: Your approval is requested: Expenses Policy") {
			t.Fatalf("mail to %s = %q", r.email, data)
		}
	}
	var sent int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_outbox WHERE workspace_id=$1 AND sent_at IS NOT NULL`, ws).Scan(&sent); err != nil || sent != 2 {
		t.Fatalf("sent outbox rows = %d (%v), want 2", sent, err)
	}
}

func TestMailLeavesOutAPrivateSpacesPageTitle_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	alice := member{"alice@corp.com", d.Member(t, ws, "alice@corp.com")}
	carol := member{"carol@corp.com", d.Member(t, ws, "carol@corp.com")}
	pageID := d.Page(t, ws, alice.id, "Layoff Plan")
	var spaceID string
	if err := d.Pool.QueryRow(ctx, `UPDATE spaces SET private = true WHERE id = (SELECT space_id FROM pages WHERE id=$1) RETURNING id`,
		pageID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	stub, got := smtpStub(t)
	sink := email.NewSink(d.Pool, stub)
	if rr := chain(d, ws, sink)(alice, "POST", "/v1/spaces/"+spaceID+"/pages/"+pageID+"/approval",
		`{"reviewers":["`+carol.id+`"]}`); rr.Code != http.StatusCreated {
		t.Fatalf("POST approval = %d %s", rr.Code, rr.Body.String())
	}
	if _, err := sink.SendDue(ctx); err != nil {
		t.Fatal(err)
	}
	if mails := got(); len(mails) != 1 || strings.Contains(mails[0], "Layoff Plan") || !strings.Contains(mails[0], "a page in a private space") {
		t.Fatalf("mails = %q, want one that does not name the private page", mails)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
