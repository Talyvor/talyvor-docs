package webhooks

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/docs/internal/authz"
	"github.com/talyvor/docs/internal/safehttp"
)

// Handler is /v1/workspaces/{wsID}/webhooks: create, list, rotate and delete, owner only.
type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/webhooks", func(r chi.Router) {
		r.Post("/", h.create)
		r.Get("/", h.list)
		r.Post("/{id}/rotate", h.rotate)
		r.Delete("/{id}", h.remove)
	})
}

// owner returns {wsID} when the verified caller is its owner (workspace_members.role); anyone else gets 403.
func owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	wsID := chi.URLParam(r, "wsID") // nosemgrep: docs-no-url-param-workspace-scope -- authorized on the next line before any store op
	m, ok := authz.AuthorizeWorkspace(r.Context(), wsID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return "", false
	}
	if m.Role != "owner" {
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return "", false
	}
	return wsID, true
}

type createResponse struct {
	*Webhook
	Secret string `json:"secret"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := owner(w, r)
	if !ok {
		return
	}
	var in struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", "bad json")
		return
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		writeErr(w, http.StatusBadRequest, "BAD_URL", "url must be an https:// URL")
		return
	}
	for _, e := range in.Events {
		if !slices.Contains(Events, e) {
			writeErr(w, http.StatusBadRequest, "BAD_EVENT", "unknown event "+e)
			return
		}
	}
	if err := safehttp.Check(r.Context(), u.Hostname()); err != nil {
		if errors.Is(err, safehttp.ErrBlockedAddress) {
			writeErr(w, http.StatusBadRequest, "BLOCKED_ADDRESS", "url must be a public address")
			return
		}
		writeErr(w, http.StatusBadRequest, "BAD_HOST", "url host does not resolve")
		return
	}
	hook, secret, err := h.store.Create(r.Context(), wsID, in.URL, in.Events)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE_FAILED", "could not save the webhook")
		return
	}
	writeJSON(w, http.StatusCreated, createResponse{Webhook: hook, Secret: secret})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	wsID, ok := owner(w, r)
	if !ok {
		return
	}
	out, err := h.store.List(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", "could not list webhooks")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) {
	wsID, ok := owner(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	secret, err := h.store.Rotate(r.Context(), wsID, id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "webhook not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ROTATE_FAILED", "could not rotate the secret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "secret": secret})
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	wsID, ok := owner(w, r)
	if !ok {
		return
	}
	err := h.store.Delete(r.Context(), wsID, chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "webhook not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", "could not delete the webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
