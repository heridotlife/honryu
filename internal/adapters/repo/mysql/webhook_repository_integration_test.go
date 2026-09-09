//go:build integration

package mysql_test

import (
	"testing"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/webhookstoretest"
	"github.com/heridotlife/honryu/test/dbtest"
)

// The MySQL adapter passes the same conformance suite as the fake, which is
// what keeps it interchangeable with the in-memory store.
func TestMySQLWebhookStore_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	webhookstoretest.Run(t, func(t *testing.T) ports.WebhookStore {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}
