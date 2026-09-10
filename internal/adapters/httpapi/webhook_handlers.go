package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/rbac"
	"github.com/heridotlife/honryu/internal/domain/webhook"
)

// webhookResponse is the JSON wire shape for a registered webhook. The
// secret is deliberately absent: it is write-only (Create accepts it, no
// response ever carries it back) -- a receiver's signing secret leaving
// the platform again would be a leak with no consumer, the same reason
// has_secret is a bool instead of an optional string.
type webhookResponse struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url"`
	HasSecret   bool      `json:"has_secret"`
	Enabled     bool      `json:"enabled"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedTime time.Time `json:"created_time"`
}

func toWebhookResponse(w webhook.Webhook) webhookResponse {
	return webhookResponse{
		ID:          w.ID,
		URL:         w.URL,
		HasSecret:   w.Secret != "",
		Enabled:     w.Enabled,
		CreatedBy:   w.CreatedBy,
		CreatedTime: w.CreatedTime,
	}
}

// webhookGate rejects the request unless the webhook service is wired. It
// returns true when the handler may proceed. The gate runs before the
// project authorization so a service-less router answers 404 "not
// configured" rather than probing projects it was never going to notify
// for -- the same contract every optional service follows.
func (h *handlers) webhookGate(w http.ResponseWriter) bool {
	if h.deps.Webhooks == nil {
		writeError(w, http.StatusNotFound, "webhooks not configured")
		return false
	}
	return true
}

// createWebhook registers a run-completion webhook for a project. The URL
// must be https: a cleartext endpoint would carry the HMAC secret's proof
// (and the payload) in the clear, so registration -- not delivery -- is
// where that is refused, with the stated reason.
func (h *handlers) createWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.webhookGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionCreate); err != nil {
		respondError(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	rawURL := strings.TrimSpace(r.PostForm.Get("url"))
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if !strings.HasPrefix(rawURL, "https://") {
		writeError(w, http.StatusBadRequest, "url must be https")
		return
	}
	created, err := h.deps.Webhooks.Create(r.Context(), projectID, rawURL, r.PostForm.Get("secret"),
		accountFrom(r.Context()).Subject)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWebhookResponse(created))
}

// listWebhooks returns a project's webhooks in registration order, both
// enabled and paused -- the pause state is the UI's to render, not the
// list's to filter on.
func (h *handlers) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if !h.webhookGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionRead); err != nil {
		respondError(w, err)
		return
	}
	hooks, err := h.deps.Webhooks.List(r.Context(), projectID)
	if err != nil {
		respondError(w, err)
		return
	}
	out := make([]webhookResponse, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, toWebhookResponse(hook))
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteWebhook removes one of a project's webhooks. The service scopes the
// delete by project, so a webhook id under a foreign project's path is a
// plain 404 -- not this project's to delete.
func (h *handlers) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.webhookGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	id, ok := pathInt(r, "webhook_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionDelete); err != nil {
		respondError(w, err)
		return
	}
	if err := h.deps.Webhooks.Delete(r.Context(), projectID, id); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// setWebhookEnabled pauses (enabled=false) or resumes (enabled=true) one of
// a project's webhooks. A paused webhook is skipped by delivery, so
// pause/resume is a plain state update, not a delivery concern.
func (h *handlers) setWebhookEnabled(w http.ResponseWriter, r *http.Request) {
	if !h.webhookGate(w) {
		return
	}
	projectID, ok := pathInt(r, "project_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	id, ok := pathInt(r, "webhook_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	if err := h.authorizeProject(r.Context(), projectID, rbac.ActionUpdate); err != nil {
		respondError(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form")
		return
	}
	enabled, err := strconv.ParseBool(r.PostForm.Get("enabled"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid enabled")
		return
	}
	if err := h.deps.Webhooks.SetEnabled(r.Context(), projectID, id, enabled); err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}
