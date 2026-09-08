package fake_test

import (
	"testing"

	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/internal/ports/sharestoretest"
)

// The fake must pass the same conformance suite as the MySQL adapter; see
// reportstore_contract_test.go for the reasoning.
func TestShareStore_Contract(t *testing.T) {
	sharestoretest.Run(t, func(*testing.T) ports.ShareStore {
		return fake.NewShareStore()
	})
}
