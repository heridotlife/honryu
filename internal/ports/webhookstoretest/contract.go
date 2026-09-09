// Package webhookstoretest is the shared conformance suite every
// WebhookStore must pass, fake and real alike.
package webhookstoretest

import (
	"context"
	"errors"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/webhook"
	"github.com/heridotlife/honryu/internal/ports"
)

// NewStore builds a store with no webhooks in it.
type NewStore func(t *testing.T) ports.WebhookStore

// Run exercises WebhookStore behaviour.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	ctx := context.Background()

	// mk builds a valid webhook for a project; tests vary the fields they
	// care about.
	mk := func(projectID int64, url string) webhook.Webhook {
		return webhook.Webhook{ProjectID: projectID, URL: url, Enabled: true, CreatedBy: "dave"}
	}

	t.Run("CreateAssignsIDAndStamp", func(t *testing.T) {
		s := newStore(t)
		id, err := s.CreateWebhook(ctx, mk(7, "https://hooks.example/1"))
		if err != nil {
			t.Fatalf("CreateWebhook: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d, want a storage-assigned positive id", id)
		}
		got, err := s.ListWebhooksByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListWebhooksByProject: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("list = %d webhooks, want 1", len(got))
		}
		if got[0].ID != id || got[0].URL != "https://hooks.example/1" {
			t.Errorf("stored = id %d url %q, want id %d url %q", got[0].ID, got[0].URL, id, "https://hooks.example/1")
		}
		if got[0].Secret != "" {
			t.Errorf("secret = %q, want empty when none was set", got[0].Secret)
		}
		if !got[0].Enabled {
			t.Errorf("enabled = false, want the registered default true")
		}
		if got[0].CreatedBy != "dave" {
			t.Errorf("created_by = %q, want %q", got[0].CreatedBy, "dave")
		}
		if got[0].CreatedTime.IsZero() {
			t.Errorf("created_time is zero; the issuing UI shows it")
		}
	})

	t.Run("SecretRoundTrips", func(t *testing.T) {
		// The secret is what signs deliveries, so its exact bytes must
		// survive storage -- not a hash, not a trim.
		s := newStore(t)
		w := mk(7, "https://hooks.example/1")
		w.Secret = "s3cret-with spaces "
		if _, err := s.CreateWebhook(ctx, w); err != nil {
			t.Fatalf("CreateWebhook: %v", err)
		}
		got, err := s.ListWebhooksByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListWebhooksByProject: %v", err)
		}
		if len(got) != 1 || got[0].Secret != w.Secret {
			t.Fatalf("secret = %q, want the exact %q", got[0].Secret, w.Secret)
		}
	})

	t.Run("ListScopesByProject", func(t *testing.T) {
		// Project 7 gets two endpoints; project 8 gets one, which must not
		// leak into 7's list nor vice versa.
		s := newStore(t)
		for _, row := range []struct {
			project int64
			url     string
		}{
			{7, "https://hooks.example/a"},
			{7, "https://hooks.example/b"},
			{8, "https://hooks.example/c"},
		} {
			if _, err := s.CreateWebhook(ctx, mk(row.project, row.url)); err != nil {
				t.Fatalf("CreateWebhook(project %d): %v", row.project, err)
			}
		}
		got, err := s.ListWebhooksByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListWebhooksByProject: %v", err)
		}
		if len(got) != 2 || got[0].URL != "https://hooks.example/a" || got[1].URL != "https://hooks.example/b" {
			t.Errorf("list(7) = %v, want 2 oldest-first", got)
		}
		other, err := s.ListWebhooksByProject(ctx, 8)
		if err != nil {
			t.Fatalf("ListWebhooksByProject(8): %v", err)
		}
		if len(other) != 1 || other[0].URL != "https://hooks.example/c" {
			t.Errorf("list(8) = %v, want only its own", other)
		}
	})

	t.Run("DeleteIsProjectScoped", func(t *testing.T) {
		s := newStore(t)
		id, err := s.CreateWebhook(ctx, mk(7, "https://hooks.example/a"))
		if err != nil {
			t.Fatalf("CreateWebhook: %v", err)
		}
		// Another project's delete must not remove it: not project 8's to
		// delete, even though the id is real.
		if err := s.DeleteWebhook(ctx, 8, id); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("DeleteWebhook(other project) = %v, want ErrNotFound", err)
		}
		if err := s.DeleteWebhook(ctx, 7, id); err != nil {
			t.Fatalf("DeleteWebhook: %v", err)
		}
		got, err := s.ListWebhooksByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListWebhooksByProject: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("list after delete = %v, want empty", got)
		}
		if err := s.DeleteWebhook(ctx, 7, id); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("deleting an unknown webhook = %v, want ErrNotFound", err)
		}
	})

	t.Run("SetEnabledRoundTripsAndScopes", func(t *testing.T) {
		s := newStore(t)
		id, err := s.CreateWebhook(ctx, mk(7, "https://hooks.example/a"))
		if err != nil {
			t.Fatalf("CreateWebhook: %v", err)
		}
		if err := s.SetWebhookEnabled(ctx, 8, id, false); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("SetWebhookEnabled(other project) = %v, want ErrNotFound", err)
		}
		if err := s.SetWebhookEnabled(ctx, 7, id, false); err != nil {
			t.Fatalf("SetWebhookEnabled: %v", err)
		}
		got, err := s.ListWebhooksByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListWebhooksByProject: %v", err)
		}
		if len(got) != 1 || got[0].Enabled {
			t.Fatalf("enabled after pause = %v, want false", got)
		}
		if err := s.SetWebhookEnabled(ctx, 7, id, true); err != nil {
			t.Fatalf("SetWebhookEnabled(resume): %v", err)
		}
		got, err = s.ListWebhooksByProject(ctx, 7)
		if err != nil {
			t.Fatalf("ListWebhooksByProject: %v", err)
		}
		if len(got) != 1 || !got[0].Enabled {
			t.Fatalf("enabled after resume = %v, want true", got)
		}
		if err := s.SetWebhookEnabled(ctx, 7, id+999, true); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("SetWebhookEnabled(unknown) = %v, want ErrNotFound", err)
		}
	})
}
