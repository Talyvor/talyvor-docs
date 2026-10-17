package email

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/talyvor/docs/internal/authz"
)

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Branding is a workspace's mail brand (B39.20). An empty field means Talyvor's default for it.
type Branding struct {
	FromName    string `json:"from_name"`
	HeaderColor string `json:"header_color"`
	LogoURL     string `json:"logo_url"`
}

// validate answers what is wrong with b, or "" when it may be stored.
func (b Branding) validate() string {
	if len(b.FromName) > 64 || strings.ContainsFunc(b.FromName, unicode.IsControl) {
		return "from_name must be at most 64 characters with no line breaks"
	}
	if b.HeaderColor != "" && !hexColor.MatchString(b.HeaderColor) {
		return "header_color must be a colour like #1E40AF"
	}
	if b.LogoURL != "" {
		u, err := url.Parse(b.LogoURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || len(b.LogoURL) > 2048 {
			return "logo_url must be an https URL"
		}
	}
	return ""
}

// BrandingHandler lets a workspace's owner set how its mail looks; members may read it.
type BrandingHandler struct{ pool *pgxpool.Pool }

func NewBrandingHandler(pool *pgxpool.Pool) *BrandingHandler { return &BrandingHandler{pool: pool} }

func (h *BrandingHandler) Mount(r chi.Router) {
	r.Get("/workspaces/{wsID}/email-branding", h.Get)
	r.Put("/workspaces/{wsID}/email-branding", h.Put)
}

// Get answers the workspace's branding, all fields empty when it set none.
func (h *BrandingHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID") // nosemgrep: docs-no-url-param-workspace-scope -- authorized on the next line before any store op
	if _, ok := authz.AuthorizeWorkspace(r.Context(), wsID); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this workspace"})
		return
	}
	var b Branding
	err := h.pool.QueryRow(r.Context(), `SELECT from_name, header_color, logo_url FROM workspace_email_branding
		WHERE workspace_id = $1`, wsID).Scan(&b.FromName, &b.HeaderColor, &b.LogoURL)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read email branding"})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// Put replaces the branding with the body, e.g. {"from_name":"Acme","header_color":"#1E40AF","logo_url":"https://…"}.
// Only the workspace's owner (workspace_members.role) may; anyone else is 403.
func (h *BrandingHandler) Put(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID") // nosemgrep: docs-no-url-param-workspace-scope -- authorized on the next line before any store op
	m, ok := authz.AuthorizeWorkspace(r.Context(), wsID)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this workspace"})
		return
	}
	if m.Role != "owner" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the workspace owner can set email branding"})
		return
	}
	var b Branding
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {from_name, header_color, logo_url}"})
		return
	}
	b.FromName = strings.TrimSpace(b.FromName)
	if msg := b.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO workspace_email_branding (workspace_id, from_name, header_color, logo_url) VALUES ($1, $2, $3, $4)
		ON CONFLICT (workspace_id) DO UPDATE SET from_name = EXCLUDED.from_name, header_color = EXCLUDED.header_color,
		    logo_url = EXCLUDED.logo_url, updated_at = NOW()`,
		wsID, b.FromName, b.HeaderColor, b.LogoURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save email branding"})
		return
	}
	writeJSON(w, http.StatusOK, b)
}
