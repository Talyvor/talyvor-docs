package email

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KindApprovalRequested is mailed to a reviewer when their approval is requested, unless they turned it off.
const KindApprovalRequested = "approval.requested"

// KindStaleDigest is the daily mail to a page owner listing their stale pages and the approvals waiting for
// them. Off unless the member turned it on.
const KindStaleDigest = "freshness.digest"

// MaxAttempts is how many times one message is tried before it is left with its last error.
const MaxAttempts = 5

// lease is how long a claimed row waits before another worker may send it again, if the first never reports.
const lease = 10 * time.Minute

type Sender interface {
	Send(ctx context.Context, b Brand, to, subject, body string) error
}

// Sink is a notifier for the approval handler: a request writes one outbox row per reviewer, and Start's loop
// sends them. It never fails the request that caused the event.
type Sink struct {
	pool        *pgxpool.Pool
	sender      Sender
	kick        chan struct{}
	defaultLogo string
}

func NewSink(pool *pgxpool.Pool, sender Sender) *Sink {
	return &Sink{pool: pool, sender: sender, kick: make(chan struct{}, 1)}
}

// WithDefaultLogo sets the logo in an unbranded workspace's mail header; only an https URL is used.
func (s *Sink) WithDefaultLogo(url string) *Sink {
	if strings.HasPrefix(url, "https://") {
		s.defaultLogo = url
	}
	return s
}

// Emit queues mail for approval.requested and ignores every other event.
func (s *Sink) Emit(ctx context.Context, wsID, eventType string, data map[string]any) {
	reqID, _ := data["request_id"].(string)
	if eventType != KindApprovalRequested || reqID == "" {
		return
	}
	// The request may end the moment this returns; the insert must not be cancelled with it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// Reads the stored request, never the event data: a reviewer is addressed only through this workspace's
	// roster, and nobody is mailed about a request they made themselves. The message is what the approvals
	// inbox already shows them; a private space's page title is not, so it stays out of the mail.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO notification_outbox (workspace_id, member_id, kind, to_email, subject, body)
		SELECT r.workspace_id, m.member_id, $3, m.email,
		       'Your approval is requested: ' || CASE WHEN sp.private THEN 'a page in a private space' ELSE p.title END,
		       coalesce((SELECT a.email FROM workspace_members a
		                 WHERE a.workspace_id = r.workspace_id AND a.member_id = r.requested_by LIMIT 1), 'Someone')
		           || ' asked you to approve ' || CASE WHEN sp.private THEN 'a page in a private space' ELSE '"' || p.title || '"' END
		           || ' in Talyvor Docs.'
		           || CASE WHEN r.message <> '' THEN E'\n\n' || r.message ELSE '' END
		           || CASE WHEN r.due_date IS NOT NULL THEN E'\n\nDue ' || to_char(r.due_date AT TIME ZONE 'UTC', 'DD Mon YYYY') ELSE '' END
		           || E'\n\nIt is waiting in your approvals inbox.'
		FROM approval_requests r
		JOIN pages p ON p.id = r.page_id AND p.workspace_id = r.workspace_id
		JOIN spaces sp ON sp.id = p.space_id AND sp.workspace_id = r.workspace_id
		JOIN review_decisions d ON d.request_id = r.id
		JOIN workspace_members m ON m.workspace_id = r.workspace_id AND m.member_id = d.reviewer_id
		WHERE r.workspace_id = $1 AND r.id = $2 AND d.reviewer_id <> r.requested_by
		  AND coalesce((SELECT np.email FROM notification_preferences np
		                WHERE np.workspace_id = m.workspace_id AND np.member_id = m.member_id AND np.kind = $3), true)`,
		wsID, reqID, KindApprovalRequested)
	if err != nil {
		slog.Error("email: enqueue", "workspace_id", wsID, "request_id", reqID, "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// QueueStaleDigest writes one digest per opted-in member of the workspace who owns one of stalePageIDs or has an
// approval waiting for them, and returns how many it queued. A member mailed a digest in the last 20 hours is
// skipped, so a second run the same day sends nothing twice. A private space's page title stays out of the mail.
func (s *Sink) QueueStaleDigest(ctx context.Context, wsID string, stalePageIDs []string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH stale AS (
		    SELECT p.created_by AS member_id, count(*) AS n,
		           string_agg('- ' || CASE WHEN sp.private THEN 'a page in a private space' ELSE '"' || p.title || '"' END,
		                      E'\n' ORDER BY p.updated_at) AS lines
		    FROM pages p
		    JOIN spaces sp ON sp.id = p.space_id AND sp.workspace_id = p.workspace_id
		    WHERE p.workspace_id = $1 AND p.id = ANY($2)
		    GROUP BY p.created_by),
		waiting AS (
		    SELECT d.reviewer_id AS member_id, count(*) AS n,
		           string_agg('- ' || CASE WHEN sp.private THEN 'a page in a private space' ELSE '"' || p.title || '"' END
		                      || CASE WHEN a.due_date IS NOT NULL THEN ' (due ' || to_char(a.due_date AT TIME ZONE 'UTC', 'DD Mon YYYY') || ')' ELSE '' END,
		                      E'\n' ORDER BY a.created_at) AS lines
		    FROM approval_requests a
		    JOIN review_decisions d ON d.request_id = a.id
		    JOIN pages p ON p.id = a.page_id AND p.workspace_id = a.workspace_id
		    JOIN spaces sp ON sp.id = p.space_id AND sp.workspace_id = a.workspace_id
		    WHERE a.workspace_id = $1 AND a.status = 'pending' AND d.decision = 'pending'
		    GROUP BY d.reviewer_id)
		INSERT INTO notification_outbox (workspace_id, member_id, kind, to_email, subject, body)
		SELECT m.workspace_id, m.member_id, $3, m.email,
		       'Your Talyvor Docs digest: ' || coalesce(st.n, 0) || CASE WHEN st.n = 1 THEN ' stale page, ' ELSE ' stale pages, ' END
		           || coalesce(w.n, 0) || CASE WHEN w.n = 1 THEN ' approval waiting' ELSE ' approvals waiting' END,
		       'Here is your daily Talyvor Docs digest.'
		           || CASE WHEN st.n > 0 THEN E'\n\nPages you own that are past their review date:\n' || st.lines ELSE '' END
		           || CASE WHEN w.n > 0 THEN E'\n\nWaiting for your approval:\n' || w.lines ELSE '' END
		           || E'\n\nYou get this because you turned on the daily digest.'
		FROM workspace_members m
		JOIN notification_preferences np
		  ON np.workspace_id = m.workspace_id AND np.member_id = m.member_id AND np.kind = $3 AND np.email
		LEFT JOIN stale st ON st.member_id = m.member_id
		LEFT JOIN waiting w ON w.member_id = m.member_id
		WHERE m.workspace_id = $1 AND (st.n IS NOT NULL OR w.n IS NOT NULL)
		  AND NOT EXISTS (SELECT 1 FROM notification_outbox o
		                  WHERE o.workspace_id = m.workspace_id AND o.member_id = m.member_id AND o.kind = $3
		                    AND o.created_at > NOW() - INTERVAL '20 hours')`,
		wsID, stalePageIDs, KindStaleDigest)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() > 0 {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
	return int(tag.RowsAffected()), nil
}

// Start sends due mail at once (which replays anything left from before a restart), then every interval and
// whenever an event is queued.
func (s *Sink) Start(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.SendDue(ctx); err != nil && ctx.Err() == nil {
			slog.Error("email: send", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

type outboxRow struct {
	id, workspaceID, to, subject, body string
	brand                              Brand
}

// SendDue sends every message that is due and returns how many it tried.
func (s *Sink) SendDue(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
		    SELECT id FROM notification_outbox
		    WHERE sent_at IS NULL AND attempts < $1 AND due_at <= NOW()
		    ORDER BY due_at LIMIT 50 FOR UPDATE SKIP LOCKED),
		claimed AS (
		    UPDATE notification_outbox SET attempts = attempts + 1, due_at = NOW() + $2 * INTERVAL '1 second'
		    WHERE id IN (SELECT id FROM due)
		    RETURNING id, workspace_id, to_email, subject, body)
		-- The brand is read at send time, so a mail queued before the owner set it still goes out branded.
		SELECT c.id, c.workspace_id, c.to_email, c.subject, c.body,
		       coalesce(b.from_name, ''), coalesce(b.header_color, ''), coalesce(b.logo_url, '')
		FROM claimed c LEFT JOIN workspace_email_branding b ON b.workspace_id = c.workspace_id`, MaxAttempts, int64(lease/time.Second))
	if err != nil {
		return 0, err
	}
	var due []outboxRow
	for rows.Next() {
		var r outboxRow
		var name, color, logo string
		if err := rows.Scan(&r.id, &r.workspaceID, &r.to, &r.subject, &r.body, &name, &color, &logo); err != nil {
			rows.Close()
			return 0, err
		}
		r.brand = s.brandFor(name, color, logo)
		due = append(due, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range due {
		if err := s.sender.Send(ctx, r.brand, r.to, r.subject, r.body); err != nil {
			slog.Warn("email: send failed", "id", r.id, "err", err)
			// Retry after 1, 2, 4, 8 minutes; the lease above covers a crash between claim and here.
			if _, uerr := s.pool.Exec(ctx, `
				UPDATE notification_outbox SET error = $3, due_at = NOW() + power(2, attempts - 1) * INTERVAL '1 minute'
				WHERE workspace_id = $1 AND id = $2`, r.workspaceID, r.id, err.Error()); uerr != nil {
				slog.Error("email: record failure", "id", r.id, "err", uerr)
			}
			continue
		}
		if _, err := s.pool.Exec(ctx, `UPDATE notification_outbox SET sent_at = NOW(), error = '' WHERE workspace_id = $1 AND id = $2`,
			r.workspaceID, r.id); err != nil {
			slog.Error("email: record sent", "id", r.id, "err", err)
		}
	}
	return len(due), nil
}
