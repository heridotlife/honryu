//go:build integration

package mysql_test

import (
	"testing"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/digeststoretest"
	"github.com/heridotlife/honryu/test/dbtest"
)

// The MySQL adapter passes the same conformance suite as the fake, which is
// what keeps it interchangeable with the in-memory store.
func TestMySQLDigestStore_Contract(t *testing.T) {
	db := dbtest.StartMySQL(t)
	digeststoretest.Run(t, func(t *testing.T) ports.ReportDigestStore {
		truncateAll(t, db)
		return mysqladapter.NewRepository(db)
	})
}
