package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/talyvor/docs/internal/safehttp"
)

// Backoff is the wait before each retry: attempt 2 waits Backoff[0], and so on. After the last one an event is
// given up. Standard Webhooks' suggested schedule, ending about 18 hours after the first attempt.
var Backoff = []time.Duration{5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour}

// Sign returns the webhook-signature value for one message: "v1," + base64 HMAC-SHA256 of
// "<id>.<timestamp>.<body>", keyed with the base64 part of a "whsec_" secret.
func Sign(secret, msgID string, ts int64, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return "", fmt.Errorf("webhooks: bad secret: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%d.", msgID, ts)
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Sink is the notifier the approval and comment handlers call: each event becomes one pending attempt per subscribed endpoint, and
// Start's loop sends them. It never fails the request that caused the event.
type Sink struct {
	store  *Store
	client *http.Client
	kick   chan struct{}
}

func NewSink(store *Store) *Sink {
	c := safehttp.Client(10 * time.Second)
	// A webhook answers where it was registered; a redirect is a failed attempt, not a new target.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Sink{store: store, client: c, kick: make(chan struct{}, 1)}
}

// WithClient replaces the SSRF-guarded client; tests use it to reach a local server.
func (s *Sink) WithClient(c *http.Client) *Sink { s.client = c; return s }

// Emit queues one event for every endpoint in wsID subscribed to eventType. data gains workspace_id.
func (s *Sink) Emit(ctx context.Context, wsID, eventType string, data map[string]any) {
	data["workspace_id"] = wsID
	body, err := json.Marshal(map[string]any{"type": eventType, "timestamp": time.Now().UTC(), "data": data})
	if err != nil {
		slog.Error("webhooks: marshal event", "type", eventType, "err", err)
		return
	}
	idBytes := make([]byte, 16)
	_, _ = rand.Read(idBytes)
	msgID := "msg_" + hex.EncodeToString(idBytes)
	// The request may end the moment this returns; the insert must not be cancelled with it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tag, err := s.store.pool.Exec(ctx, `
		INSERT INTO workspace_webhook_deliveries (workspace_id, webhook_id, message_id, event_type, payload, attempt)
		SELECT workspace_id, id, $2, $3, $4, 1 FROM workspace_webhooks
		WHERE workspace_id = $1 AND (cardinality(events) = 0 OR $3 = ANY(events))`,
		wsID, msgID, eventType, string(body))
	if err != nil {
		slog.Error("webhooks: enqueue", "workspace_id", wsID, "type", eventType, "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// Start sends due attempts every interval, and at once after an event is queued.
func (s *Sink) Start(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.DeliverDue(ctx); err != nil && ctx.Err() == nil {
			slog.Error("webhooks: deliver", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

type attempt struct {
	id, workspaceID, webhookID, msgID, url, payload string
	n                                               int
}

// DeliverDue sends every attempt that is due and returns how many it sent. Claiming a row stamps attempted_at,
// so two Docs instances never send the same attempt.
func (s *Sink) DeliverDue(ctx context.Context) (int, error) {
	// shortcut: an attempt claimed by a process that dies before recording the answer is never retried;
	// sweep rows with attempted_at set and no status_code/error if that is ever seen.
	rows, err := s.store.pool.Query(ctx, `
		WITH due AS (
		    SELECT id FROM workspace_webhook_deliveries
		    WHERE attempted_at IS NULL AND due_at <= NOW()
		    ORDER BY due_at LIMIT 50 FOR UPDATE SKIP LOCKED),
		claimed AS (
		    UPDATE workspace_webhook_deliveries SET attempted_at = NOW()
		    WHERE id IN (SELECT id FROM due)
		    RETURNING id, workspace_id, webhook_id, message_id, payload, attempt)
		SELECT c.id, c.workspace_id, c.webhook_id, c.message_id, w.url, c.payload, c.attempt
		FROM claimed c JOIN workspace_webhooks w ON w.id = c.webhook_id`)
	if err != nil {
		return 0, err
	}
	var due []attempt
	for rows.Next() {
		var a attempt
		if err := rows.Scan(&a.id, &a.workspaceID, &a.webhookID, &a.msgID, &a.url, &a.payload, &a.n); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Concurrently, so one slow endpoint holds up no other workspace's events.
	var wg sync.WaitGroup
	for _, a := range due {
		wg.Add(1)
		go func() { defer wg.Done(); s.send(ctx, a) }()
	}
	wg.Wait()
	return len(due), nil
}

func (s *Sink) send(ctx context.Context, a attempt) {
	status, sendErr := s.post(ctx, a)
	ok := sendErr == nil && status >= 200 && status < 300
	errText := ""
	if sendErr != nil {
		errText = sendErr.Error()
	} else if !ok {
		errText = "HTTP " + strconv.Itoa(status)
	}
	var code *int
	if status != 0 {
		code = &status
	}
	if _, err := s.store.pool.Exec(ctx,
		`UPDATE workspace_webhook_deliveries SET status_code = $3, error = $4 WHERE workspace_id = $1 AND id = $2`,
		a.workspaceID, a.id, code, errText); err != nil {
		slog.Error("webhooks: record attempt", "id", a.id, "err", err)
	}
	// A refused address will be refused again; retrying it only hammers the guard.
	if ok || errors.Is(sendErr, safehttp.ErrBlockedAddress) || a.n > len(Backoff) {
		return
	}
	if _, err := s.store.pool.Exec(ctx, `
		INSERT INTO workspace_webhook_deliveries
		    (workspace_id, webhook_id, message_id, event_type, payload, attempt, due_at)
		SELECT workspace_id, webhook_id, message_id, event_type, payload, attempt + 1, NOW() + $3 * INTERVAL '1 second'
		FROM workspace_webhook_deliveries WHERE workspace_id = $1 AND id = $2
		ON CONFLICT (webhook_id, message_id, attempt) DO NOTHING`,
		a.workspaceID, a.id, int64(Backoff[a.n-1]/time.Second)); err != nil {
		slog.Error("webhooks: schedule retry", "id", a.id, "err", err)
	}
}

func (s *Sink) post(ctx context.Context, a attempt) (int, error) {
	secret, err := s.store.secretFor(ctx, a.workspaceID, a.webhookID)
	if err != nil {
		return 0, err
	}
	ts := time.Now().Unix()
	sig, err := Sign(secret, a.msgID, ts, []byte(a.payload))
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader([]byte(a.payload)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Talyvor-Docs-Webhooks")
	req.Header.Set("webhook-id", a.msgID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("webhook-signature", sig)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}
