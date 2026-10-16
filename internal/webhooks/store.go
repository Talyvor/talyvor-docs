// Package webhooks sends a workspace's approval and comment events to the URLs its owner registered, signed the
// Standard Webhooks way (headers webhook-id, webhook-timestamp, webhook-signature) exactly as Track does, so a
// customer verifies both once. Each attempt is a row in workspace_webhook_deliveries; a failure is retried with
// backoff and a 2xx is never sent again.
package webhooks

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a webhook id does not exist in the workspace.
var ErrNotFound = errors.New("webhooks: not found")

// Events are the event types an endpoint can subscribe to.
var Events = []string{
	"approval.requested", "approval.decided", "comment.created",
}

// Webhook is the non-secret view: the secret is returned only by Create and Rotate.
type Webhook struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	pool   *pgxpool.Pool
	cipher *Cipher
}

func NewStore(pool *pgxpool.Pool, c *Cipher) *Store {
	return &Store{pool: pool, cipher: c}
}

// newSecret is a Standard Webhooks secret: "whsec_" + base64 of 24 random bytes.
func newSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b), nil
}

// Create registers url for events (empty = all) and returns the webhook with its secret, shown once.
func (s *Store) Create(ctx context.Context, workspaceID, url string, events []string) (*Webhook, string, error) {
	secret, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	ct, nonce, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return nil, "", fmt.Errorf("webhooks: seal: %w", err)
	}
	if events == nil {
		events = []string{}
	}
	w := &Webhook{URL: url, Events: events}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO workspace_webhooks (workspace_id, url, events, secret_ciphertext, secret_nonce)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		workspaceID, url, events, ct, nonce).Scan(&w.ID, &w.CreatedAt)
	if err != nil {
		return nil, "", fmt.Errorf("webhooks: create: %w", err)
	}
	return w, secret, nil
}

func (s *Store) List(ctx context.Context, workspaceID string) ([]Webhook, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, url, events, created_at FROM workspace_webhooks
		WHERE workspace_id = $1 ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.URL, &w.Events, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Rotate replaces the secret and returns the new one. Attempts not yet sent are signed with it.
func (s *Store) Rotate(ctx context.Context, workspaceID, id string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	ct, nonce, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("webhooks: seal: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE workspace_webhooks SET secret_ciphertext = $3, secret_nonce = $4, updated_at = NOW()
		WHERE workspace_id = $1 AND id = $2`, workspaceID, id, ct, nonce)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return secret, nil
}

// Delete removes the webhook and, by cascade, its delivery rows.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_webhooks WHERE workspace_id = $1 AND id = $2`, workspaceID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// secretFor reads and unseals a webhook's current secret.
func (s *Store) secretFor(ctx context.Context, workspaceID, webhookID string) (string, error) {
	var ct, nonce []byte
	err := s.pool.QueryRow(ctx,
		`SELECT secret_ciphertext, secret_nonce FROM workspace_webhooks WHERE workspace_id = $1 AND id = $2`,
		workspaceID, webhookID).Scan(&ct, &nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	pt, err := s.cipher.Decrypt(ct, nonce)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
