package fake_test

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/digestschedulestoretest"
	"github.com/heridotlife/honryu/internal/ports/fake"
)

// The in-memory store passes the same conformance suite as the MySQL
// adapter, which is what keeps it interchangeable with the real one.
func TestDigestScheduleStore_Contract(t *testing.T) {
	digestschedulestoretest.Run(t, func(*testing.T) ports.DigestScheduleStore {
		return fake.NewDigestScheduleStore()
	})
}
