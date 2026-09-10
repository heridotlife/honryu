package fake_test

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/digeststoretest"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// The in-memory store passes the same conformance suite as the MySQL
// adapter, which is what keeps it interchangeable with the real one.
func TestDigestStore_Contract(t *testing.T) {
	digeststoretest.Run(t, func(*testing.T) ports.ReportDigestStore {
		return fake.NewDigestStore()
	})
}
