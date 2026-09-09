package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports"
)

// WebhookStore is an in-memory ports.WebhookStore for fast use-case tests.
type WebhookStore struct {
	mu       sync.Mutex
	webhooks map[int64]webhook.Webhook
	seq      int64

	// CreateErr, when set, is returned by CreateWebhook.
	CreateErr error
	// ListErr, when set, is returned by ListWebhooksByProject.
	ListErr error
	// DeleteErr, when set, is returned by DeleteWebhook.
	DeleteErr error
	// SetEnabledErr, when set, is returned by SetWebhookEnabled.
	SetEnabledErr error
}

// NewWebhookStore builds an empty store.
func NewWebhookStore() *WebhookStore {
	return &WebhookStore{webhooks: map[int64]webhook.Webhook{}}
}

var _ ports.WebhookStore = (*WebhookStore)(nil)

// CreateWebhook records a registration, assigning the row id and stamp.
func (s *WebhookStore) CreateWebhook(_ context.Context, w webhook.Webhook) (int64, error) {
	if s.CreateErr != nil {
		return 0, s.CreateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	w.ID = s.seq
	w.CreatedTime = time.Now().UTC()
	s.webhooks[w.ID] = w
	return w.ID, nil
}

// ListWebhooksByProject returns the project's webhooks, oldest first.
func (s *WebhookStore) ListWebhooksByProject(_ context.Context, projectID int64) ([]webhook.Webhook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ListErr != nil {
		return nil, s.ListErr
	}
	var out []webhook.Webhook
	for _, w := range s.webhooks {
		if w.ProjectID == projectID {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeleteWebhook removes one webhook, or ports.ErrNotFound when it does not
// exist under projectID.
func (s *WebhookStore) DeleteWebhook(_ context.Context, projectID, id int64) error {
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.webhooks[id]
	if !ok || w.ProjectID != projectID {
		return ports.ErrNotFound
	}
	delete(s.webhooks, id)
	return nil
}

// SetWebhookEnabled pauses or resumes one webhook, or ports.ErrNotFound
// when it does not exist under projectID.
func (s *WebhookStore) SetWebhookEnabled(_ context.Context, projectID, id int64, enabled bool) error {
	if s.SetEnabledErr != nil {
		return s.SetEnabledErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.webhooks[id]
	if !ok || w.ProjectID != projectID {
		return ports.ErrNotFound
	}
	w.Enabled = enabled
	s.webhooks[id] = w
	return nil
}
