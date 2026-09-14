//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	mysqladapter "github.com/heridotlife/honryu/internal/adapters/repo/mysql"
	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/test/dbtest"
)

// The repositories below had no closed-database sweep yet, so their first
// DB-error branch of every method was untested. Each op must return an error
// once the pool is closed -- these document the failure is surfaced, not
// silently swallowed behind a nil check nobody wrote.
func TestMySQLCalibration_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	now := time.Now()
	ops := map[string]func() error{
		"CreateCalibrationJob": func() error { _, e := repo.CreateCalibrationJob(ctx, 1, 0); return e },
		"GetCalibrationJob":    func() error { _, e := repo.GetCalibrationJob(ctx, 1); return e },
		"ListCalibrationJobsByExecution": func() error {
			_, e := repo.ListCalibrationJobsByExecution(ctx, 1)
			return e
		},
		"ClaimNextStep": func() error {
			_, _, e := repo.ClaimNextStep(ctx, now, time.Minute)
			return e
		},
		"RecordStep": func() error {
			return repo.RecordStep(ctx, 1, calibration.Step{}, ports.CalibrationJob{})
		},
		"StepsFor": func() error {
			_, e := repo.StepsFor(ctx, 1)
			return e
		},
		"MarkFailed": func() error { return repo.MarkFailed(ctx, 1, "boom") },
		"SetCalibrationBounds": func() error {
			return repo.SetCalibrationBounds(ctx, 1, ports.CalibrationBounds{SeedQPS: 1, MaxQPS: 2, MaxSteps: 3, HoldSeconds: 4})
		},
		"CalibrationBoundsFor": func() error {
			_, e := repo.CalibrationBoundsFor(ctx, 1)
			return e
		},
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}

func TestMySQLCampaign_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	now := time.Now()
	c := campaign.Campaign{Name: "c", TenantID: 1, Window: campaign.Window{Start: now, End: now.Add(time.Hour)}}
	ops := map[string]func() error{
		"CreateCampaign":        func() error { _, e := repo.CreateCampaign(ctx, c); return e },
		"GetCampaign":           func() error { _, e := repo.GetCampaign(ctx, 1); return e },
		"ListCampaignsByTenant": func() error { _, e := repo.ListCampaignsByTenant(ctx, 1); return e },
		"ListActiveCampaigns":   func() error { _, e := repo.ListActiveCampaigns(ctx, now); return e },
		"AbortCampaign":         func() error { return repo.AbortCampaign(ctx, 1, now) },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}

func TestMySQLClusterRegistry_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	c := clusterregistry.Cluster{Name: "c", Namespace: "honryu", Origin: clusterregistry.OriginOperator, CreatedBy: "op"}
	ops := map[string]func() error{
		"CreateCluster":             func() error { return repo.CreateCluster(ctx, c) },
		"GetCluster":                func() error { _, e := repo.GetCluster(ctx, "c"); return e },
		"ListClusters":              func() error { _, e := repo.ListClusters(ctx); return e },
		"UpdateCluster":             func() error { return repo.UpdateCluster(ctx, c) },
		"DeleteCluster":             func() error { return repo.DeleteCluster(ctx, "c") },
		"ResolveCluster":            func() error { _, e := repo.ResolveCluster(ctx, "c"); return e },
		"SetClusterCredential":      func() error { return repo.SetClusterCredential(ctx, "c", []byte("ct")) },
		"GetClusterCredential":      func() error { _, e := repo.GetClusterCredential(ctx, "c"); return e },
		"SetClusterIngestTokenHash": func() error { return repo.SetClusterIngestTokenHash(ctx, "c", "h") },
		"ClusterByIngestTokenHash":  func() error { _, e := repo.ClusterByIngestTokenHash(ctx, "h"); return e },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}

// Capacity profiles, orphan completions and reservations are small enough to
// sweep together on one sacrificed container.
func TestMySQLCapacityOrphanReservation_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	now := time.Now()
	code := 0
	profile := capacityprofile.CapacityProfile{
		Key:       capacityprofile.Key{ScenarioID: 1, Engine: taurus.ExecutorJMeter, CPU: "1", Memory: "512Mi"},
		PerPodQPS: 30, SaturatedBy: calibration.SaturatedByEngine,
		ScenarioFingerprint: "fp1", CalibratedAt: now, JobID: 1,
	}
	ops := map[string]func() error{
		"UpsertCapacityProfile": func() error { return repo.UpsertCapacityProfile(ctx, profile) },
		"GetCapacityProfile": func() error {
			_, e := repo.GetCapacityProfile(ctx, profile.Key)
			return e
		},
		"ListCapacityProfiles": func() error {
			_, e := repo.ListCapacityProfiles(ctx)
			return e
		},
		"RecordOrphanCompletion": func() error {
			return repo.RecordOrphanCompletion(ctx, ports.OrphanCompletion{
				ExecutionID: 1, ScenarioID: 1, ShardIndex: 0, ExitCode: &code, FinishedAt: now,
			})
		},
		"OrphanCompletions": func() error { _, e := repo.OrphanCompletions(ctx, 1); return e },
		"ClearOrphanCompletions": func() error {
			return repo.ClearOrphanCompletions(ctx, 1)
		},
		"WithTenantLock": func() error {
			return repo.WithTenantLock(ctx, 1, "home", func(context.Context) error { return nil })
		},
		"CreateReservation": func() error {
			_, e := repo.CreateReservation(ctx, reservation.Reservation{
				TenantID: 1, Cluster: "home", EngineCount: 1, Start: now, End: now.Add(time.Hour), ExecutionID: 1,
			})
			return e
		},
		"DeleteReservation": func() error { return repo.DeleteReservation(ctx, 1) },
		"ReleaseReservationsForExecution": func() error {
			return repo.ReleaseReservationsForExecution(ctx, 1)
		},
		"ReservationsInWindow": func() error {
			_, e := repo.ReservationsInWindow(ctx, 1, "home", now, now.Add(time.Hour))
			return e
		},
		"ReservationsForTenant": func() error {
			_, e := repo.ReservationsForTenant(ctx, 1, "home")
			return e
		},
		"GetCeiling": func() error {
			_, e := repo.GetCeiling(ctx, 1, "home")
			return e
		},
		"SetCeiling": func() error { return repo.SetCeiling(ctx, 1, "home", 3) },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}

// The scenario/execution sweep predates StoreExecutionConfig and the
// correlation-ID methods; these close the remaining first-error branches.
func TestMySQLExecutionConfig_ErrorsWhenDBClosed(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	ops := map[string]func() error{
		"ExecutionsWithActiveRunOnCluster": func() error {
			_, e := repo.ExecutionsWithActiveRunOnCluster(ctx, "home")
			return e
		},
		"StoreExecutionConfig": func() error {
			return repo.StoreExecutionConfig(ctx, 1, false, nil, nil)
		},
		"SetExecutionCriteria": func() error { return repo.SetExecutionCriteria(ctx, 1, nil) },
		"CriteriaFor":          func() error { _, e := repo.CriteriaFor(ctx, 1); return e },
		"SetPendingCorrelationID": func() error {
			return repo.SetPendingCorrelationID(ctx, 1, "trace-1")
		},
		"PendingCorrelationID": func() error { _, e := repo.PendingCorrelationID(ctx, 1); return e },
	}
	for name, op := range ops {
		if err := op(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func newExecution(t *testing.T, repo *mysqladapter.Repository, ctx context.Context) int64 {
	t.Helper()
	pid, err := repo.CreateProject(ctx, project.Project{Name: "p"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	eid, err := repo.CreateExecution(ctx, execution.Execution{Name: "e", ProjectID: pid})
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return eid
}

// TestMySQLExecutionConfig_RejectsValuesTheSchemaCannotStore drives the
// mid-transaction failure branches of the config writers: an unsigned
// throughput or an over-long criterion makes the INSERT fail after the tx has
// already begun, which must roll back rather than half-commit.
func TestMySQLExecutionConfig_RejectsValuesTheSchemaCannotStore(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	eid := newExecution(t, repo, ctx)

	steps := []struct {
		name string
		err  error
	}{
		{"StoreLoadProfile negative throughput", repo.StoreLoadProfile(ctx, eid, false,
			[]loadprofile.Entry{{ScenarioID: 1, Throughput: -1}})},
		{"StoreExecutionConfig negative throughput", repo.StoreExecutionConfig(ctx, eid, false,
			[]loadprofile.Entry{{ScenarioID: 1, Throughput: -1}}, nil)},
		{"StoreExecutionConfig over-long criterion", repo.StoreExecutionConfig(ctx, eid, false, nil,
			[]string{strings.Repeat("x", 300)})},
		{"SetExecutionCriteria over-long criterion", repo.SetExecutionCriteria(ctx, eid,
			[]string{strings.Repeat("y", 300)})},
		{"StoreLoadProfile missing execution", repo.StoreLoadProfile(ctx, 1<<40, false, nil)},
		{"StoreExecutionConfig missing execution", repo.StoreExecutionConfig(ctx, 1<<40, false, nil, nil)},
		{"SetPendingCorrelationID missing execution", repo.SetPendingCorrelationID(ctx, 1<<40, "t")},
	}
	for _, step := range steps {
		if step.err == nil {
			t.Errorf("%s: want error, got nil", step.name)
		}
	}

	if _, err := repo.PendingCorrelationID(ctx, 1<<40); err != ports.ErrNotFound {
		t.Errorf("PendingCorrelationID missing execution: got %v, want ports.ErrNotFound", err)
	}
	// The failed writers must not have left a half-applied profile behind.
	entries, err := repo.LoadProfileFor(ctx, eid)
	if err != nil || len(entries) != 0 {
		t.Errorf("load profile after failed writes = %v, %v; want empty, nil", entries, err)
	}
}

// TestMySQLCalibration_DeepErrorPaths drives failure branches past the first
// statement of each method by mangling the schema on a sacrificed container:
// a renamed column makes the next statement in the transaction fail, which is
// the only way to reach rollback paths the closed-pool sweep cannot.
func TestMySQLCalibration_DeepErrorPaths(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	eid := newExecution(t, repo, ctx)

	jobID, err := repo.CreateCalibrationJob(ctx, eid, 0)
	if err != nil {
		t.Fatalf("create calibration job: %v", err)
	}

	// No FK joins calibration_job_step to calibration_job, so a step against a
	// missing job inserts and then updates zero rows -- surfaced as ErrNotFound.
	if err := repo.RecordStep(ctx, 1<<40, calibration.Step{}, ports.CalibrationJob{Phase: calibration.PhaseBisecting}); err != ports.ErrNotFound {
		t.Errorf("RecordStep missing job: got %v, want ports.ErrNotFound", err)
	}
	if err := repo.MarkFailed(ctx, 1<<40, "boom"); err != ports.ErrNotFound {
		t.Errorf("MarkFailed missing job: got %v, want ports.ErrNotFound", err)
	}

	mustExec(t, db, "ALTER TABLE calibration_job_step RENAME COLUMN seq TO seq_renamed")
	if err := repo.RecordStep(ctx, jobID, calibration.Step{}, ports.CalibrationJob{Phase: calibration.PhaseBisecting}); err == nil {
		t.Error("RecordStep with renamed seq column: want error, got nil")
	}

	mustExec(t, db, "ALTER TABLE calibration_job_step RENAME COLUMN seq_renamed TO seq")
	mustExec(t, db, "DROP TABLE calibration_job")
	if err := repo.RecordStep(ctx, jobID, calibration.Step{}, ports.CalibrationJob{Phase: calibration.PhaseBisecting}); err == nil {
		t.Error("RecordStep with dropped calibration_job: want error, got nil")
	}
	if _, _, err := repo.ClaimNextStep(ctx, time.Now(), time.Minute); err == nil {
		t.Error("ClaimNextStep with dropped calibration_job: want error, got nil")
	}
}

// TestMySQLCampaign_DeepErrorPaths reaches the scan and service-loading
// branches past each method's first statement by mangling the schema.
func TestMySQLCampaign_DeepErrorPaths(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	now := time.Now()

	base := campaign.Campaign{
		Name: "c", TenantID: 1,
		Window:   campaign.Window{Start: now, End: now.Add(2 * time.Hour)},
		Services: []campaign.Service{{ProjectID: 1, ExecutionID: 2}},
	}
	id, err := repo.CreateCampaign(ctx, base)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	// The services insert is the second statement of CreateCampaign's tx.
	mustExec(t, db, "ALTER TABLE campaign_service RENAME COLUMN project_id TO project_id_renamed")
	if _, err := repo.CreateCampaign(ctx, base); err == nil {
		t.Error("CreateCampaign with renamed campaign_service column: want error, got nil")
	}
	mustExec(t, db, "ALTER TABLE campaign_service RENAME COLUMN project_id_renamed TO project_id")

	// A NULL name cannot come through the domain (Validate), but a row only
	// the database can produce must fail the scan loudly, not panic.
	mustExec(t, db, "ALTER TABLE campaign MODIFY name VARCHAR(200) NULL")
	mustExec(t, db, "INSERT INTO campaign (name, tenant_id, window_start, window_end) VALUES (NULL, 1, ?, ?)",
		now, now.Add(time.Hour))
	if _, err := repo.ListCampaignsByTenant(ctx, 1); err == nil {
		t.Error("ListCampaignsByTenant with NULL name row: want error, got nil")
	}
	if _, err := repo.ListActiveCampaigns(ctx, now.Add(time.Minute)); err == nil {
		t.Error("ListActiveCampaigns with NULL name row: want error, got nil")
	}
	mustExec(t, db, "DELETE FROM campaign WHERE name IS NULL")

	// With the services table gone, every campaign listing path must surface
	// the failure rather than return campaigns that look service-free.
	mustExec(t, db, "DROP TABLE campaign_service")
	if _, err := repo.GetCampaign(ctx, id); err == nil {
		t.Error("GetCampaign with dropped campaign_service: want error, got nil")
	}
	if _, err := repo.ListCampaignsByTenant(ctx, 1); err == nil {
		t.Error("ListCampaignsByTenant with dropped campaign_service: want error, got nil")
	}
}

// TestMySQLClusterRegistry_ListScansUnmappableRow keeps the row-mapping
// honest: a NULL in a column the domain types as string must error the scan.
func TestMySQLClusterRegistry_ListScansUnmappableRow(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()

	if err := repo.CreateCluster(ctx, clusterregistry.Cluster{
		Name: "ok", Namespace: "honryu", Origin: clusterregistry.OriginOperator,
		CreatedBy: "op", CreatedTime: time.Now(),
	}); err != nil {
		t.Fatalf("create cluster: %v", err)
	}

	mustExec(t, db, "ALTER TABLE cluster_registry MODIFY api_url VARCHAR(512) NULL")
	mustExec(t, db, `INSERT INTO cluster_registry
		(name, api_url, ca_cert, ingest_url, sidecar_image, namespace, secret_ref, origin, created_by, created_time)
		VALUES ('bad', NULL, '', '', '', 'honryu', '', 'operator', 'op', NOW(6))`)
	if _, err := repo.ListClusters(ctx); err == nil {
		t.Error("ListClusters with NULL api_url row: want error, got nil")
	}
	if _, err := repo.GetCluster(ctx, "ok"); err != nil {
		t.Errorf("GetCluster healthy row after mangling: %v", err)
	}
}

func progressBatch(seq int64, stream string) ports.ProgressBatch {
	iv := metrics.Interval{
		Seq: seq, Timestamp: 1000, Label: "checkout",
		Samples: 1, Concurrency: 2,
		Errors: []metrics.ErrorGroup{{Message: "boom", ResponseCode: "500", Count: 1}},
	}
	code := 0
	b := ports.ProgressBatch{
		RunID: 1, ScenarioID: 1, ShardIndex: 0, StreamID: stream,
		Intervals: []metrics.Interval{iv},
	}
	if seq > 1 {
		b.Final, b.ExitCode = true, &code
	}
	return b
}

// TestMySQLReportProgress_AbsorbFailsMidTransaction reaches the branches
// inside Absorb's transaction that a closed pool cannot: closing fails
// BeginTx, while a sacrificed table or column fails a specific later
// statement, proving the error propagates and the tx rolls back.
func TestMySQLReportProgress_AbsorbFailsMidTransaction(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		mangle func(t *testing.T, db *sql.DB, repo *mysqladapter.Repository)
	}{
		{"shard table gone", func(t *testing.T, db *sql.DB, _ *mysqladapter.Repository) {
			mustExec(t, db, "DROP TABLE report_progress_shard")
		}},
		{"shard finished column gone", func(t *testing.T, db *sql.DB, repo *mysqladapter.Repository) {
			if err := repo.Absorb(ctx, progressBatch(1, "s")); err != nil {
				t.Fatalf("seed absorb: %v", err)
			}
			mustExec(t, db, "ALTER TABLE report_progress_shard DROP COLUMN finished")
		}},
		{"second table gone", func(t *testing.T, db *sql.DB, repo *mysqladapter.Repository) {
			if err := repo.Absorb(ctx, progressBatch(1, "s")); err != nil {
				t.Fatalf("seed absorb: %v", err)
			}
			mustExec(t, db, "DROP TABLE report_progress_second")
		}},
		{"label table gone", func(t *testing.T, db *sql.DB, _ *mysqladapter.Repository) {
			mustExec(t, db, "DROP TABLE report_progress_label")
		}},
		{"signature table gone", func(t *testing.T, db *sql.DB, _ *mysqladapter.Repository) {
			mustExec(t, db, "DROP TABLE report_progress_signature")
		}},
		{"label latency column renamed", func(t *testing.T, db *sql.DB, repo *mysqladapter.Repository) {
			if err := repo.Absorb(ctx, progressBatch(1, "s")); err != nil {
				t.Fatalf("seed absorb: %v", err)
			}
			mustExec(t, db, "ALTER TABLE report_progress_label RENAME COLUMN latency TO latency_renamed")
		}},
		{"label failed column gone", func(t *testing.T, db *sql.DB, repo *mysqladapter.Repository) {
			if err := repo.Absorb(ctx, progressBatch(1, "s")); err != nil {
				t.Fatalf("seed absorb: %v", err)
			}
			mustExec(t, db, "ALTER TABLE report_progress_label DROP COLUMN failed")
		}},
		{"signature exemplars column renamed", func(t *testing.T, db *sql.DB, repo *mysqladapter.Repository) {
			if err := repo.Absorb(ctx, progressBatch(1, "s")); err != nil {
				t.Fatalf("seed absorb: %v", err)
			}
			mustExec(t, db, "ALTER TABLE report_progress_signature RENAME COLUMN exemplars TO exemplars_renamed")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtest.StartMySQL(t)
			repo := mysqladapter.NewRepository(db)
			tc.mangle(t, db, repo)
			if err := repo.Absorb(ctx, progressBatch(2, "s")); err == nil {
				t.Fatal("Absorb against mangled schema: want error, got nil")
			}
			// A failed Absorb must not leave a committed watermark behind:
			// seq 2 would make a retry of the same batch a silent no-op.
			states, err := repo.ShardStates(ctx, 1)
			if err == nil {
				for _, st := range states {
					if st.Finished {
						t.Error("shard marked finished despite failed Absorb")
					}
				}
			}
		})
	}
}

// TestMySQLReportProgress_SnapshotFailsOnUnmappableRows drives Snapshot's,
// ShardStates' and Discard's per-table failure branches on one container:
// each phase mangles one table, asserts the error, then repairs or drops it.
func TestMySQLReportProgress_SnapshotFailsOnUnmappableRows(t *testing.T) {
	db := dbtest.StartMySQL(t)
	repo := mysqladapter.NewRepository(db)
	ctx := context.Background()
	if err := repo.Absorb(ctx, progressBatch(1, "s")); err != nil {
		t.Fatalf("seed absorb: %v", err)
	}

	mustExec(t, db, "ALTER TABLE report_progress_label MODIFY samples BIGINT UNSIGNED NULL")
	mustExec(t, db, "UPDATE report_progress_label SET samples = NULL WHERE run_id = 1")
	if _, err := repo.Snapshot(ctx, 1); err == nil {
		t.Error("Snapshot with NULL samples: want error, got nil")
	}

	mustExec(t, db, "UPDATE report_progress_label SET samples = 0, latency = '{}' WHERE run_id = 1")
	mustExec(t, db, "ALTER TABLE report_progress_second MODIFY engine_concurrency BIGINT NULL")
	mustExec(t, db, "UPDATE report_progress_second SET engine_concurrency = NULL WHERE run_id = 1")
	if _, err := repo.Snapshot(ctx, 1); err == nil {
		t.Error("Snapshot with NULL engine_concurrency: want error, got nil")
	}

	mustExec(t, db, "UPDATE report_progress_second SET engine_concurrency = 0 WHERE run_id = 1")
	mustExec(t, db, "ALTER TABLE report_progress_signature MODIFY count BIGINT UNSIGNED NULL")
	mustExec(t, db, "UPDATE report_progress_signature SET count = NULL WHERE run_id = 1")
	if _, err := repo.Snapshot(ctx, 1); err == nil {
		t.Error("Snapshot with NULL count: want error, got nil")
	}

	mustExec(t, db, "UPDATE report_progress_signature SET count = 0 WHERE run_id = 1")
	mustExec(t, db, "DROP TABLE report_progress_second")
	if _, err := repo.Snapshot(ctx, 1); err == nil {
		t.Error("Snapshot with second table gone: want error, got nil")
	}
	if err := repo.Absorb(ctx, progressBatch(2, "s")); err == nil {
		t.Error("Absorb with second table gone: want error, got nil")
	}

	mustExec(t, db, "DROP TABLE report_progress_signature")
	if _, err := repo.Snapshot(ctx, 1); err == nil {
		t.Error("Snapshot with signature table gone: want error, got nil")
	}

	mustExec(t, db, "ALTER TABLE report_progress_shard MODIFY finished TINYINT(1) NULL")
	mustExec(t, db, "UPDATE report_progress_shard SET finished = NULL WHERE run_id = 1")
	if _, err := repo.ShardStates(ctx, 1); err == nil {
		t.Error("ShardStates with NULL finished: want error, got nil")
	}
}

// TestMigrate_DeepErrorPaths feeds Migrate a schema_migrations it cannot
// trust: the table always exists, but its shape or contents make reading the
// applied versions fail -- Migrate must refuse rather than re-apply anything.
func TestMigrate_DeepErrorPaths(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup []string
	}{
		{
			name:  "no version column",
			setup: []string{"CREATE TABLE schema_migrations (x INT)"},
		},
		{
			name: "NULL version row",
			setup: []string{
				"CREATE TABLE schema_migrations (version VARCHAR(255) NULL)",
				"INSERT INTO schema_migrations (version) VALUES (NULL)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("mysql", dbtest.StartMySQLDSN(t))
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, stmt := range tc.setup {
				mustExec(t, db, stmt)
			}
			if err := mysqladapter.Migrate(ctx, db); err == nil {
				t.Fatal("Migrate against unusable schema_migrations: want error, got nil")
			}
		})
	}
}

// TestMigrate_RejectsConflictingBaseline reaches the apply-migration failure
// branch: execution_scenario already carrying the column 0017 adds makes the
// ALTER fail, and Migrate must stop there rather than soldier on.
func TestMigrate_RejectsConflictingBaseline(t *testing.T) {
	db, err := sql.Open("mysql", dbtest.StartMySQLDSN(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mustExec(t, db, `CREATE TABLE execution_scenario (
		scenario_id BIGINT UNSIGNED NOT NULL,
		execution_id INT UNSIGNED NOT NULL,
		concurrency INT UNSIGNED NOT NULL DEFAULT 0,
		rampup INT UNSIGNED NOT NULL DEFAULT 0,
		duration INT UNSIGNED NOT NULL DEFAULT 0,
		engines INT NOT NULL DEFAULT 1,
		throughput INT UNSIGNED NOT NULL DEFAULT 0,
		csv_split TINYINT(1) NOT NULL DEFAULT 0,
		PRIMARY KEY (execution_id, scenario_id)
	) CHARSET=utf8mb4`)
	if err := mysqladapter.Migrate(context.Background(), db); err == nil {
		t.Fatal("Migrate with conflicting execution_scenario baseline: want error, got nil")
	}
}
