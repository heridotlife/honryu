package fake

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/thresholdstoretest"
)

// The in-memory store passes the same conformance suite the MySQL adapter
// must, which is what keeps them interchangeable.
func TestThresholdStore_Contract(t *testing.T) {
	thresholdstoretest.Run(t, func(t *testing.T) ports.ThresholdStore {
		return NewThresholdStore()
	})
}
