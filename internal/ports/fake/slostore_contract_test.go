package fake_test

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/internal/ports/slotest"
)

// The fake passes the same conformance suite the MySQL adapter must, which
// is what keeps the two interchangeable.
func TestFakeSLOStore_Contract(t *testing.T) {
	t.Parallel()
	slotest.Run(t, func(t *testing.T) ports.SLOStore {
		t.Helper()
		return fake.NewSLOStore()
	})
}
