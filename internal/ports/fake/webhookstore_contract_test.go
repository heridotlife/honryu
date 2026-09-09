package fake_test

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/internal/ports/webhookstoretest"
)

// The in-memory store passes the same conformance suite as the MySQL
// adapter, which is what keeps it interchangeable with the real one.
func TestWebhookStore_Contract(t *testing.T) {
	webhookstoretest.Run(t, func(*testing.T) ports.WebhookStore {
		return fake.NewWebhookStore()
	})
}
