package email

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/talyvor/docs/internal/authz"
)

// defaults is each kind a member may set and what it is when they have no row (migrations/0026).
var defaults = map[string]bool{KindApprovalRequested: true, KindStaleDigest: false}

// PrefsHandler lets a member read and switch their own email in a workspace. The member is always the caller's
// verified membership there; no member id is read from the request.
type PrefsHandler struct{ pool *pgxpool.Pool }

func NewPrefsHandler(pool *pgxpool.Pool) *PrefsHandler { return &PrefsHandler{pool: pool} }

func (h *PrefsHandler) Mount(r chi.Router) {
	r.Get("/workspaces/{wsID}/notification-preferences", h.Get)
	r.Put("/workspaces/{wsID}/notification-preferences", h.Put)
}

func (h *PrefsHandler) caller(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	wsID := chi.URLParam(r, "wsID") // nosemgrep: docs-no-url-param-workspace-scope -- authorized on the next line before any store op
	m, ok := authz.AuthorizeWorkspace(r.Context(), wsID)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this workspace"})
		return "", "", false
	}
	return wsID, m.MemberID, true
}

// Get answers {"approval.requested": bool, "freshness.digest": bool} for the caller.
func (h *PrefsHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID, memberID, ok := h.caller(w, r)
	if !ok {
		return
	}
	out := map[string]bool{}
	for k, v := range defaults {
		out[k] = v
	}
	rows, err := h.pool.Query(r.Context(), `SELECT kind, email FROM notification_preferences
		WHERE workspace_id = $1 AND member_id = $2`, wsID, memberID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read preferences"})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var on bool
		if err := rows.Scan(&kind, &on); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read preferences"})
			return
		}
		if _, known := defaults[kind]; known {
			out[kind] = on
		}
	}
	if rows.Err() != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read preferences"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Put sets the kinds named in the body, e.g. {"freshness.digest": true}, and answers like Get. Any other key is 400.
func (h *PrefsHandler) Put(w http.ResponseWriter, r *http.Request) {
	wsID, memberID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var body map[string]bool
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must map approval.requested or freshness.digest to true or false"})
		return
	}
	var kinds []string
	var ons []bool
	for kind, on := range body {
		if _, known := defaults[kind]; !known {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown notification kind: " + kind})
			return
		}
		kinds, ons = append(kinds, kind), append(ons, on)
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO notification_preferences (workspace_id, member_id, kind, email)
		SELECT $1, $2, k, e FROM unnest($3::text[], $4::bool[]) AS t(k, e)
		ON CONFLICT (workspace_id, member_id, kind) DO UPDATE SET email = EXCLUDED.email, updated_at = NOW()`,
		wsID, memberID, kinds, ons); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save preferences"})
		return
	}
	h.Get(w, r)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
