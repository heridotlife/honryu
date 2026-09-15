package ports

import (
	"context"

	"github.com/heridotlife/honryu/internal/domain/slo"
)

// SLOStore persists the service-level objectives registered per project.
//
// An SLO is a projection of an operator's intent, not an aggregate with
// invariants spanning rows: create validates the domain object (at least one
// target, name bounds) and the store records it. Scoping is by project
// everywhere -- an SLO is only ever read or deleted through the project it
// belongs to, which is what stops one project's operator from touching
// another's objectives even before HTTP authorization gets involved.
type SLOStore interface {
	// CreateSLO stores s and returns its storage-assigned id. The caller
	// has already validated s. A name that already exists under the
	// project is a duplicate: implementations return the driver's own
	// duplicate-key error (the 0066 unique key enforces it); the use-case
	// layer pre-checks so callers get the domain's message.
	CreateSLO(ctx context.Context, s slo.SLO) (int64, error)
	// ListSLOsByProject returns the project's SLOs in definition order
	// (oldest first).
	ListSLOsByProject(ctx context.Context, projectID int64) ([]slo.SLO, error)
	// GetSLO returns one SLO, or ErrNotFound when no such SLO exists under
	// projectID (including when it exists under a different project: not
	// this project's to read).
	GetSLO(ctx context.Context, projectID, id int64) (slo.SLO, error)
	// DeleteSLO removes one SLO, or ErrNotFound under the same
	// project-scoping rule as GetSLO.
	DeleteSLO(ctx context.Context, projectID, id int64) error
}
