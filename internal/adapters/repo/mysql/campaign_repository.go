package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/ports"
)

var _ ports.CampaignRepository = (*Repository)(nil)

const campaignColumns = "id, name, tenant_id, window_start, window_end, aborted_at"

// CreateCampaign inserts c and every entry of c.Services in one transaction
// -- a campaign with no services would violate its own domain invariant, so
// the two must commit together or not at all -- and returns c's
// auto-assigned ID.
func (r *Repository) CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		"INSERT INTO campaign (name, tenant_id, window_start, window_end, aborted_at) VALUES (?, ?, ?, ?, ?)",
		c.Name, c.TenantID, c.Window.Start, c.Window.End, nullPtr(c.AbortedAt))
	if err != nil {
		return 0, fmt.Errorf("mysql: create campaign: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mysql: create campaign last id: %w", err)
	}
	for _, svc := range c.Services {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO campaign_service (campaign_id, project_id, execution_id) VALUES (?, ?, ?)",
			id, svc.ProjectID, svc.ExecutionID); err != nil {
			return 0, fmt.Errorf("mysql: create campaign service: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("mysql: commit create campaign: %w", err)
	}
	return id, nil
}

// GetCampaign returns the campaign with id, its services included, or
// ports.ErrNotFound.
func (r *Repository) GetCampaign(ctx context.Context, id int64) (campaign.Campaign, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+campaignColumns+" FROM campaign WHERE id = ?", id)
	c, err := scanCampaign(row)
	if errors.Is(err, sql.ErrNoRows) {
		return campaign.Campaign{}, ports.ErrNotFound
	}
	if err != nil {
		return campaign.Campaign{}, fmt.Errorf("mysql: get campaign: %w", err)
	}
	c.Services, err = r.servicesForCampaign(ctx, id)
	if err != nil {
		return campaign.Campaign{}, err
	}
	return c, nil
}

// ListCampaignsByTenant returns every campaign belonging to tenantID,
// ordered by window start, each with its services included.
func (r *Repository) ListCampaignsByTenant(ctx context.Context, tenantID int64) ([]campaign.Campaign, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+campaignColumns+" FROM campaign WHERE tenant_id = ? ORDER BY window_start", tenantID)
	if err != nil {
		return nil, fmt.Errorf("mysql: list campaigns: %w", err)
	}
	out, err := scanCampaigns(rows)
	if err != nil {
		return nil, err
	}
	return r.withServices(ctx, out)
}

// ListAllCampaigns returns every campaign across every tenant, ordered
// by window start -- the service-provider admin view.
func (r *Repository) ListAllCampaigns(ctx context.Context) ([]campaign.Campaign, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+campaignColumns+" FROM campaign ORDER BY window_start")
	if err != nil {
		return nil, fmt.Errorf("mysql: list all campaigns: %w", err)
	}
	return scanCampaigns(rows)
}

// ListCampaignsByTenants returns every campaign belonging to any of
// tenantIDs, ordered by window start, each with its services included. An
// empty tenantIDs yields an empty list -- the shape GET /api/campaigns
// relies on for an account that holds no tenant role at all.
func (r *Repository) ListCampaignsByTenants(ctx context.Context, tenantIDs []int64) ([]campaign.Campaign, error) {
	out := []campaign.Campaign{}
	if len(tenantIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(tenantIDs))
	args := make([]any, len(tenantIDs))
	for i, id := range tenantIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	// #nosec G201 -- placeholders are fixed "?" tokens; tenant ids are bound params.
	query := fmt.Sprintf("SELECT "+campaignColumns+" FROM campaign WHERE tenant_id IN (%s) ORDER BY window_start",
		strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: list campaigns by tenants: %w", err)
	}
	out, err = scanCampaigns(rows)
	if err != nil {
		return nil, err
	}
	return r.withServices(ctx, out)
}

// ListActiveCampaigns returns every campaign whose window contains now and
// which has not been aborted, each with its services included.
func (r *Repository) ListActiveCampaigns(ctx context.Context, now time.Time) ([]campaign.Campaign, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+campaignColumns+" FROM campaign WHERE window_start <= ? AND window_end > ? AND aborted_at IS NULL",
		now, now)
	if err != nil {
		return nil, fmt.Errorf("mysql: list active campaigns: %w", err)
	}
	out, err := scanCampaigns(rows)
	if err != nil {
		return nil, err
	}
	return r.withServices(ctx, out)
}

// AbortCampaign records that the campaign with id was aborted at t, or
// ports.ErrNotFound.
//
// RowsAffected alone cannot distinguish "id does not exist" from "aborted_at
// was already exactly t" -- MySQL does not count a no-op SET as an affected
// row, and Abort is documented as idempotent (a second abort of the same
// campaign must not error). A supplementary existence check disambiguates,
// paid only on the rare zero-rows path.
func (r *Repository) AbortCampaign(ctx context.Context, id int64, t time.Time) error {
	res, err := r.db.ExecContext(ctx, "UPDATE campaign SET aborted_at = ? WHERE id = ?", t, id)
	if err != nil {
		return fmt.Errorf("mysql: abort campaign: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: abort campaign rows affected: %w", err)
	}
	if n > 0 {
		return nil
	}
	var exists int
	err = r.db.QueryRowContext(ctx, "SELECT 1 FROM campaign WHERE id = ?", id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mysql: abort campaign existence check: %w", err)
	}
	return nil
}

// UpdateCampaign replaces the stored definition of the campaign with c.ID
// -- name, window, and participating services -- in one transaction,
// mirroring CreateCampaign's shape: the campaign row is updated, the
// existing campaign_service rows are deleted, and the new set inserted,
// so a stale service row can never survive an edit. AbortedAt and
// tenant_id are deliberately not written: AbortCampaign owns the former,
// and a campaign never changes tenants.
//
// RowsAffected on the UPDATE cannot distinguish "no such campaign" from
// "nothing changed" (MySQL does not count a no-op SET as an affected
// row) -- the same ambiguity AbortCampaign resolves with a supplementary
// existence check, paid only on the zero-rows path.
func (r *Repository) UpdateCampaign(ctx context.Context, c campaign.Campaign) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mysql: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		"UPDATE campaign SET name = ?, window_start = ?, window_end = ? WHERE id = ?",
		c.Name, c.Window.Start, c.Window.End, c.ID)
	if err != nil {
		return fmt.Errorf("mysql: update campaign: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mysql: update campaign rows affected: %w", err)
	}
	if n == 0 {
		var exists int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM campaign WHERE id = ?", c.ID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("mysql: update campaign existence check: %w", err)
		}
		// The row exists and the UPDATE was a no-op; the service set is
		// still replaced below, exactly as it would be after a real change.
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM campaign_service WHERE campaign_id = ?", c.ID); err != nil {
		return fmt.Errorf("mysql: delete campaign services: %w", err)
	}
	for _, svc := range c.Services {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO campaign_service (campaign_id, project_id, execution_id) VALUES (?, ?, ?)",
			c.ID, svc.ProjectID, svc.ExecutionID); err != nil {
			return fmt.Errorf("mysql: update campaign service: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mysql: commit update campaign: %w", err)
	}
	return nil
}

func (r *Repository) withServices(ctx context.Context, campaigns []campaign.Campaign) ([]campaign.Campaign, error) {
	for i := range campaigns {
		services, err := r.servicesForCampaign(ctx, campaigns[i].ID)
		if err != nil {
			return nil, err
		}
		campaigns[i].Services = services
	}
	return campaigns, nil
}

func (r *Repository) servicesForCampaign(ctx context.Context, campaignID int64) ([]campaign.Service, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT project_id, execution_id FROM campaign_service WHERE campaign_id = ? ORDER BY id", campaignID)
	if err != nil {
		return nil, fmt.Errorf("mysql: campaign services: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []campaign.Service{}
	for rows.Next() {
		var svc campaign.Service
		if err := rows.Scan(&svc.ProjectID, &svc.ExecutionID); err != nil {
			return nil, fmt.Errorf("mysql: scan campaign service: %w", err)
		}
		out = append(out, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate campaign services: %w", err)
	}
	return out, nil
}

func scanCampaigns(rows *sql.Rows) ([]campaign.Campaign, error) {
	defer func() { _ = rows.Close() }()
	out := []campaign.Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("mysql: scan campaign: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate campaigns: %w", err)
	}
	return out, nil
}

func scanCampaign(s rowScanner) (campaign.Campaign, error) {
	var (
		c         campaign.Campaign
		abortedAt sql.NullTime
	)
	if err := s.Scan(&c.ID, &c.Name, &c.TenantID, &c.Window.Start, &c.Window.End, &abortedAt); err != nil {
		return campaign.Campaign{}, err
	}
	if abortedAt.Valid {
		t := abortedAt.Time
		c.AbortedAt = &t
	}
	return c, nil
}
