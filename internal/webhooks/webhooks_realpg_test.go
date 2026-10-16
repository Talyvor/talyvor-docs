package webhooks_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/docs/internal/approval"
	"github.com/talyvor/docs/internal/authz"
	"github.com/talyvor/docs/internal/comment"
	"github.com/talyvor/docs/internal/page"
	"github.com/talyvor/docs/internal/permission"
	"github.com/talyvor/docs/internal/safehttp"
	"github.com/talyvor/docs/internal/space"
	"github.com/talyvor/docs/internal/testutil"
	"github.com/talyvor/docs/internal/webhooks"
)

func newStore(t *testing.T, d *testutil.DB) *webhooks.Store {
	t.Helper()
	c, err := webhooks.NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return webhooks.NewStore(d.Pool, c)
}

type member struct{ email, id, role string }

func seedMember(t *testing.T, d *testutil.DB, wsID, email, role string) member {
	t.Helper()
	id := d.Member(t, wsID, email)
	if _, err := d.Pool.Exec(context.Background(),
		`UPDATE workspace_members SET role = $3 WHERE workspace_id = $1 AND email = $2`, wsID, email, role); err != nil {
		t.Fatal(err)
	}
	return member{email, id, role}
}

// chain mounts the approval, comment and webhook handlers with cmd/docs/main.go's page enforcer, behind a
// boundary that installs the caller's verified membership.
func chain(d *testutil.DB, wsID string, store *webhooks.Store, sink *webhooks.Sink) func(m member, method, path, body string) *httptest.ResponseRecorder {
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
					[]authz.Membership{{WorkspaceID: wsID, MemberID: m.id, Role: m.role}})
				next.ServeHTTP(w, rq.WithContext(c))
			})
		})
		r.Route("/v1", func(r chi.Router) {
			approval.NewHandler(approval.NewStore(d.Pool)).WithAccess(pageEnf).WithNotifier(sink).Mount(r)
			comment.NewHandler(comment.NewStore(d.Pool)).WithAccess(pageEnf).WithNotifier(sink).Mount(r)
			webhooks.NewHandler(store).Mount(r)
		})
		rq := httptest.NewRequest(method, path, strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, rq)
		return rr
	}
}

type received struct {
	headers http.Header
	body    []byte
}

// receiver answers each request with the next status in statuses (the last one repeats).
func receiver(t *testing.T, statuses ...int) (*httptest.Server, func() []received) {
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{r.Header.Clone(), b})
		status := statuses[min(len(got), len(statuses))-1]
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received { mu.Lock(); defer mu.Unlock(); return append([]received(nil), got...) }
}

// verify checks a delivery the way a customer would, from the Standard Webhooks spec, not via webhooks.Sign.
func verify(t *testing.T, secret string, r received) {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		t.Fatalf("secret is not whsec_<base64>: %q", secret)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(r.headers.Get("webhook-id") + "." + r.headers.Get("webhook-timestamp") + "." + string(r.body)))
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if got := r.headers.Get("webhook-signature"); got != want {
		t.Fatalf("webhook-signature = %q, want %q", got, want)
	}
}

func attempts(t *testing.T, d *testutil.DB, wsID string) (codes []int) {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT coalesce(status_code, 0) FROM workspace_webhook_deliveries WHERE workspace_id=$1 ORDER BY attempt`, wsID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c int
		_ = rows.Scan(&c)
		codes = append(codes, c)
	}
	return codes
}

func TestApprovalRequestReachesURLOnceAndSignatureVerifies_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	alice := seedMember(t, d, ws, "alice@corp.com", "member")
	carol := seedMember(t, d, ws, "carol@corp.com", "member")
	var spaceID string
	pageID := d.Page(t, ws, alice.id, "Expenses Policy")
	if err := d.Pool.QueryRow(context.Background(), `SELECT space_id FROM pages WHERE id=$1`, pageID).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	srv, got := receiver(t, http.StatusOK)
	store := newStore(t, d)
	_, secret, err := store.Create(context.Background(), ws, srv.URL, []string{"approval.requested"})
	if err != nil {
		t.Fatal(err)
	}
	sink := webhooks.NewSink(store).WithClient(srv.Client())
	call := chain(d, ws, store, sink)
	base := "/v1/spaces/" + spaceID + "/pages/" + pageID

	rr := call(alice, "POST", base+"/approval", `{"reviewers":["`+carol.id+`"],"message":"please review"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST approval = %d %s", rr.Code, rr.Body.String())
	}
	// A comment is not subscribed: it must queue nothing for this endpoint.
	if rr := call(alice, "POST", base+"/comments", `{"content":"hi"}`); rr.Code != http.StatusCreated {
		t.Fatalf("POST comment = %d %s", rr.Code, rr.Body.String())
	}
	for range 3 { // later runs must find nothing left to send
		if _, err := sink.DeliverDue(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("receiver got %d requests, want exactly 1", len(reqs))
	}
	verify(t, secret, reqs[0])
	var ev struct {
		Type string `json:"type"`
		Data struct {
			WorkspaceID string `json:"workspace_id"`
			ActorID     string `json:"actor_id"`
			Request     struct {
				PageID string `json:"page_id"`
			} `json:"request"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reqs[0].body, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "approval.requested" || ev.Data.WorkspaceID != ws || ev.Data.ActorID != alice.id || ev.Data.Request.PageID != pageID {
		t.Fatalf("payload = %s", reqs[0].body)
	}
	if codes := attempts(t, d, ws); len(codes) != 1 || codes[0] != 200 {
		t.Fatalf("attempt rows = %v, want one 200", codes)
	}
}

func TestFailedDeliveryIsRetriedWithTheSameID_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	srv, got := receiver(t, http.StatusInternalServerError, http.StatusOK)
	store := newStore(t, d)
	_, secret, err := store.Create(context.Background(), ws, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	sink := webhooks.NewSink(store).WithClient(srv.Client())
	ctx := context.Background()

	sink.Emit(ctx, ws, "approval.decided", map[string]any{"request_id": "r1", "decision": "approved"})
	if _, err := sink.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sink.DeliverDue(ctx); n != 0 {
		t.Fatalf("retry was sent before its backoff (%d)", n)
	}
	// Bring the scheduled retry forward instead of waiting out the backoff.
	if _, err := d.Pool.Exec(ctx, `UPDATE workspace_webhook_deliveries SET due_at = NOW() WHERE attempted_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := sink.DeliverDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	reqs := got()
	if len(reqs) != 2 {
		t.Fatalf("receiver got %d requests, want 2 (a 500, then its retry)", len(reqs))
	}
	if a, b := reqs[0].headers.Get("webhook-id"), reqs[1].headers.Get("webhook-id"); a == "" || a != b {
		t.Fatalf("retry webhook-id %q, first %q: must be the same message", b, a)
	}
	verify(t, secret, reqs[1])
	if codes := attempts(t, d, ws); len(codes) != 2 || codes[0] != 500 || codes[1] != 200 {
		t.Fatalf("attempt rows = %v, want [500 200]", codes)
	}
}

func TestLoopbackURLIsRefused_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	owner := seedMember(t, d, ws, "owner@corp.com", "owner")
	store := newStore(t, d)
	call := chain(d, ws, store, webhooks.NewSink(store))

	if err := safehttp.Check(context.Background(), "127.0.0.1"); !errors.Is(err, safehttp.ErrBlockedAddress) {
		t.Fatalf("Check(127.0.0.1) = %v, want ErrBlockedAddress", err)
	}
	rr := call(owner, "POST", "/v1/workspaces/"+ws+"/webhooks", `{"url":"https://127.0.0.1/hook"}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "BLOCKED_ADDRESS") {
		t.Fatalf("create loopback = %d %s, want 400 BLOCKED_ADDRESS", rr.Code, rr.Body.String())
	}

	// A name can resolve to loopback after it was registered: the send itself is refused, and not retried.
	srv, got := receiver(t, http.StatusOK)
	if _, _, err := store.Create(context.Background(), ws, srv.URL, nil); err != nil {
		t.Fatal(err)
	}
	sink := webhooks.NewSink(store) // the real SSRF-guarded client
	sink.Emit(context.Background(), ws, "comment.created", map[string]any{"comment_id": "c1"})
	if _, err := sink.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got()) != 0 {
		t.Fatal("the guarded client reached a 127.0.0.1 server")
	}
	var total int
	var errText string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*), max(error) FROM workspace_webhook_deliveries WHERE workspace_id=$1`, ws).Scan(&total, &errText); err != nil {
		t.Fatal(err)
	}
	if total != 1 || !strings.Contains(errText, safehttp.ErrBlockedAddress.Error()) {
		t.Fatalf("rows = %d, error %q: want one refused attempt and no retry", total, errText)
	}
}

func TestOnlyAnOwnerManagesWebhooks_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	dev := seedMember(t, d, ws, "dev@corp.com", "member")
	owner := seedMember(t, d, ws, "owner@corp.com", "owner")
	store := newStore(t, d)
	call := chain(d, ws, store, webhooks.NewSink(store))
	base := "/v1/workspaces/" + ws + "/webhooks"
	body := `{"url":"https://93.184.215.14/hook","events":["approval.requested"]}` // a public literal: no DNS needed

	if rr := call(dev, "POST", base, body); rr.Code != http.StatusForbidden {
		t.Fatalf("member create = %d, want 403", rr.Code)
	}
	if rr := call(dev, "GET", base, ""); rr.Code != http.StatusForbidden {
		t.Fatalf("member list = %d, want 403", rr.Code)
	}
	if list, _ := store.List(context.Background(), ws); len(list) != 0 {
		t.Fatalf("a refused create stored %d webhooks", len(list))
	}

	rr := call(owner, "POST", base, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner create = %d %s", rr.Code, rr.Body.String())
	}
	var created struct{ ID, Secret string }
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if !strings.HasPrefix(created.Secret, "whsec_") {
		t.Fatalf("create returned secret %q", created.Secret)
	}
	if rr := call(owner, "GET", base, ""); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), created.Secret) {
		t.Fatalf("list = %d %s: must succeed and never show the secret", rr.Code, rr.Body.String())
	}
	if rr := call(dev, "POST", base+"/"+created.ID+"/rotate", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("member rotate = %d, want 403", rr.Code)
	}
	rr = call(owner, "POST", base+"/"+created.ID+"/rotate", "")
	var rotated struct{ Secret string }
	_ = json.Unmarshal(rr.Body.Bytes(), &rotated)
	if rr.Code != http.StatusOK || rotated.Secret == created.Secret || !strings.HasPrefix(rotated.Secret, "whsec_") {
		t.Fatalf("rotate = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(dev, "DELETE", base+"/"+created.ID, ""); rr.Code != http.StatusForbidden {
		t.Fatalf("member delete = %d, want 403", rr.Code)
	}
	if rr := call(owner, "DELETE", base+"/"+created.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("owner delete = %d", rr.Code)
	}
	if list, _ := store.List(context.Background(), ws); len(list) != 0 {
		t.Fatalf("after delete %d webhooks remain", len(list))
	}
}
