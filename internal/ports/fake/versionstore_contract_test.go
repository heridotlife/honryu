package fake

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/scenarioversionstoretest"
)

// The in-memory store passes the same conformance suite the MySQL adapter
// must, which is what keeps them interchangeable.
func TestVersionStore_Contract(t *testing.T) {
	scenarioversionstoretest.Run(t, func(t *testing.T) ports.ScenarioVersionStore {
		return NewVersionStore()
	})
}
