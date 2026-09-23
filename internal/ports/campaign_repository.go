package ports

import (
	"context"
	"time"

	"github.com/heridotlife/honryu/internal/domain/campaign"
)

// CampaignRepository persists campaigns and the services participating in
// them -- the record that turns "which project is in this readiness event,
// and which execution speaks for it" into something queryable rather than
// implicit.
type CampaignRepository interface {
	// CreateCampaign persists c, including every entry of c.Services, and
	// returns its assigned ID.
	CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error)
	// GetCampaign returns the campaign with id, or ErrNotFound.
	GetCampaign(ctx context.Context, id int64) (campaign.Campaign, error)
	// ListCampaignsByTenant returns every campaign belonging to tenantID,
	// ordered by window start.
	ListCampaignsByTenant(ctx context.Context, tenantID int64) ([]campaign.Campaign, error)
	// ListAllCampaigns returns every campaign across every tenant -- the
	// service-provider admin's cross-tenant view, mirroring
	// ListAllProjects. Ordered by window start.
	ListAllCampaigns(ctx context.Context) ([]campaign.Campaign, error)

	// ListCampaignsByTenants returns every campaign belonging to any of
	// tenantIDs, ordered by window start -- the cross-tenant view behind
	// GET /api/campaigns, where a campaign manager coordinates several
	// tenants and needs one list, not one request per tenant. An empty
	// tenantIDs yields an empty list.
	ListCampaignsByTenants(ctx context.Context, tenantIDs []int64) ([]campaign.Campaign, error)
	// ListActiveCampaigns returns every campaign whose window contains now
	// and which has not been aborted -- what both lifecycleapp's freeze
	// check and cmd/scheduler's drain sweep scan.
	ListActiveCampaigns(ctx context.Context, now time.Time) ([]campaign.Campaign, error)
	// UpdateCampaign replaces the stored definition of the campaign with
	// c.ID -- name, window, and participating services -- atomically: stale
	// campaign_service rows are dropped and the new set inserted in the
	// same transaction, so an orphaned service row can never survive an
	// edit. The campaign's identity (id, tenant) and AbortedAt are not
	// editable here (AbortCampaign owns the latter). Returns ErrNotFound
	// when no campaign has c.ID.
	UpdateCampaign(ctx context.Context, c campaign.Campaign) error
	// AbortCampaign records that the campaign with id was aborted at t, or
	// ErrNotFound. Idempotent in effect (aborting an already-aborted
	// campaign again just overwrites AbortedAt), since Abort's caller
	// (adminapp) does not need to distinguish "already closed" from
	// "closed by this call."
	AbortCampaign(ctx context.Context, id int64, t time.Time) error
}
