// Package fake provides in-memory implementations of the repository ports for
// fast, hermetic unit tests of the application layer. A single Store backs all
// aggregates so cross-aggregate rules (e.g. "is this scenario used by a
// execution") behave like the real shared database. Behaviour is pinned by the
// conformance suites in internal/ports/repositorytest.
package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/calibration"
	"github.com/heridotlife/honryu/internal/domain/campaign"
	"github.com/heridotlife/honryu/internal/domain/capacityprofile"
	"github.com/heridotlife/honryu/internal/domain/clusterregistry"
	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/reservation"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/schedule"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/domain/tenant"
	"github.com/heridotlife/honryu/internal/ports"
)

// Store is an in-memory implementation of every repository port.
type Store struct {
	mu  sync.Mutex
	now func() time.Time

	// namedLocks backs WithTenantLock/WithScheduleLock: a lock per key,
	// distinct from mu, since fn typically calls back into other Store
	// methods that themselves lock mu -- reusing mu here would self-deadlock.
	namedLocksMu sync.Mutex
	namedLocks   map[string]*sync.Mutex

	// MarkRunningErr, when set, is returned by MarkScenarioRunning. It lets a
	// test drive the lifecycle's roll-back path, which fires when no scenario of
	// a run could be marked running.
	MarkRunningErr error

	projectSeq int64
	projects   map[int64]project.Project

	planSeq          int64
	scenarios        map[int64]scenario.Scenario
	planTest         map[int64]string              // scenarioID -> JMX filename
	planData         map[int64]map[string]struct{} // scenarioID -> data filenames
	scenarioRequests map[int64][]byte              // scenarioID -> raw requests fragment

	collSeq      int64
	executions   map[int64]execution.Execution
	execData     map[int64]map[string]struct{} // executionID -> data filenames
	exec         map[int64][]loadprofile.Entry // executionID -> execution scenarios
	execCriteria map[int64][]string            // executionID -> configured Taurus pass/fail criteria
	// pendingCorrelation is the trace id the latest Deploy minted, waiting for
	// the next Trigger to stamp it onto a run.
	pendingCorrelation map[int64]string
	// lastActivity is the engine idle clock (execution.last_activity_at):
	// restamped by every lifecycle event that proves the engines are in use.
	// touchCount mirrors it as a per-execution call count, so a test can pin
	// WHICH lifecycle path stamped the clock, not just that something did.
	lastActivity map[int64]time.Time
	touchCount   map[int64]int

	runSeq     int64
	currentRun map[int64]int64               // executionID -> active runID
	runHistory map[int64]*ports.RunRecord    // runID -> history row
	running    map[int64]map[int64]time.Time // executionID -> scenarioID -> startedTime
	// orphans holds orphaned shard completions (Finals with no open run),
	// the evidence Trigger refuses to open a corpse-run against.
	orphans map[int64]map[orphanKey]ports.OrphanCompletion

	deployContext string
	openLaunch    map[int64]*ports.LaunchRecord // executionID -> open launch
	launchHistory []*ports.LaunchRecord

	tenantSeq int64
	tenants   map[int64]tenant.Tenant
	// grants holds role assignments, deduped by subject/role/tenant, with
	// the store's stamp for when each was recorded.
	grants []grantRecord

	reservationSeq int64
	reservations   map[int64]reservation.Reservation
	quotaCeilings  map[quotaKey]int // (tenantID, cluster) -> ceiling; absent means unconfigured

	scheduleSeq   int64
	schedules     map[int64]schedule.Schedule
	occurrenceSeq int64
	occurrences   map[int64]ports.Occurrence
	// horizonRunAt is when the horizon-extension pass last completed
	// successfully; nil means it has never run.
	horizonRunAt *time.Time

	campaignSeq int64
	campaigns   map[int64]campaign.Campaign

	calibrationJobSeq int64
	calibrationJobs   map[int64]ports.CalibrationJob
	calibrationSteps  map[int64][]calibration.Step // jobID -> steps, in order taken
	// calibrationClaimedAt is the claim lease ClaimNextStep/RecordStep/
	// MarkFailed manage -- internal bookkeeping, never part of the returned
	// ports.CalibrationJob, mirroring the mysql adapter's own claimed_at
	// column (never selected into the struct it returns either). Absent or
	// zero means unclaimed.
	calibrationClaimedAt map[int64]time.Time

	// capacityProfiles is keyed by capacityprofile.Key directly -- the key
	// IS the profile's identity, so upsert-by-key is a plain map write.
	capacityProfiles map[capacityprofile.Key]capacityprofile.CapacityProfile

	calibrationBounds map[int64]ports.CalibrationBounds // executionID -> search bounds

	// clusters is the registered-cluster registry, keyed by name.
	clusters map[string]clusterregistry.Cluster
	// clusterCredentials holds each BYOC cluster's opaque credential
	// ciphertext, keyed by name -- the fake's analogue of the mysql
	// byoc_credential BLOB, stored verbatim.
	clusterCredentials map[string][]byte

	// Embedded rather than reimplemented: a run's report and its working state
	// are keyed by run id alone, with no cross-aggregate rule tying them to the
	// rest of Store the way scenarios and executions tie to each other. The
	// same holds for share links (they address runs by token) and webhooks
	// (they address projects by id): plain keyed records, embedded as-is.
	*ReportProgress
	*ReportStore
	*ShareStore
	*WebhookStore
	*DigestStore
	*DigestScheduleStore
}

// NewStore returns an empty in-memory Store.
func NewStore() *Store {
	return &Store{
		now:                  time.Now,
		namedLocks:           make(map[string]*sync.Mutex),
		projects:             make(map[int64]project.Project),
		scenarios:            make(map[int64]scenario.Scenario),
		planTest:             make(map[int64]string),
		planData:             make(map[int64]map[string]struct{}),
		scenarioRequests:     make(map[int64][]byte),
		executions:           make(map[int64]execution.Execution),
		execData:             make(map[int64]map[string]struct{}),
		exec:                 make(map[int64][]loadprofile.Entry),
		execCriteria:         make(map[int64][]string),
		pendingCorrelation:   make(map[int64]string),
		lastActivity:         make(map[int64]time.Time),
		touchCount:           make(map[int64]int),
		currentRun:           make(map[int64]int64),
		runHistory:           make(map[int64]*ports.RunRecord),
		running:              make(map[int64]map[int64]time.Time),
		deployContext:        "default",
		openLaunch:           make(map[int64]*ports.LaunchRecord),
		tenants:              make(map[int64]tenant.Tenant),
		reservations:         make(map[int64]reservation.Reservation),
		quotaCeilings:        make(map[quotaKey]int),
		schedules:            make(map[int64]schedule.Schedule),
		occurrences:          make(map[int64]ports.Occurrence),
		campaigns:            make(map[int64]campaign.Campaign),
		calibrationJobs:      make(map[int64]ports.CalibrationJob),
		calibrationSteps:     make(map[int64][]calibration.Step),
		calibrationClaimedAt: make(map[int64]time.Time),
		capacityProfiles:     make(map[capacityprofile.Key]capacityprofile.CapacityProfile),
		calibrationBounds:    make(map[int64]ports.CalibrationBounds),
		clusters:             make(map[string]clusterregistry.Cluster),
		clusterCredentials:   make(map[string][]byte),
		ReportProgress:       NewReportProgress(),
		ReportStore:          NewReportStore(),
		ShareStore:           NewShareStore(),
		WebhookStore:         NewWebhookStore(),
		DigestStore:          NewDigestStore(),
		DigestScheduleStore:  NewDigestScheduleStore(),
	}
}

// NewProjectRepository returns a Store viewed as a ProjectRepository.
func NewProjectRepository() *Store { return NewStore() }

// withNamedLock runs fn while holding the lock for key, creating it on first
// use. Backs WithTenantLock/WithScheduleLock: a real concurrency primitive,
// not a no-op, so a test racing two goroutines against the fake store
// exercises the same serialization the MySQL adapter provides via named
// locks.
func (s *Store) withNamedLock(key string, fn func() error) error {
	s.namedLocksMu.Lock()
	lock, ok := s.namedLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		s.namedLocks[key] = lock
	}
	s.namedLocksMu.Unlock()

	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// SetNow overrides the clock used for created/started timestamps, for
// deterministic tests.
func (s *Store) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

var (
	_ ports.ProjectRepository         = (*Store)(nil)
	_ ports.ScenarioRepository        = (*Store)(nil)
	_ ports.ExecutionRepository       = (*Store)(nil)
	_ ports.RunRepository             = (*Store)(nil)
	_ ports.UsageRepository           = (*Store)(nil)
	_ ports.TenantRepository          = (*Store)(nil)
	_ ports.RoleAssignmentRepository  = (*Store)(nil)
	_ ports.ReportProgress            = (*Store)(nil)
	_ ports.ReportStore               = (*Store)(nil)
	_ ports.ShareStore                = (*Store)(nil)
	_ ports.WebhookStore              = (*Store)(nil)
	_ ports.ReportDigestStore         = (*Store)(nil)
	_ ports.DigestScheduleStore       = (*Store)(nil)
	_ ports.ReservationRepository     = (*Store)(nil)
	_ ports.CampaignRepository        = (*Store)(nil)
	_ ports.CalibrationJobRepository  = (*Store)(nil)
	_ ports.CapacityProfileRepository = (*Store)(nil)
	_ ports.ClusterRegistry           = (*Store)(nil)
)

// --- Projects ---------------------------------------------------------------

func (s *Store) CreateProject(_ context.Context, p project.Project) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectSeq++
	p.ID = s.projectSeq
	if p.CreatedTime.IsZero() {
		p.CreatedTime = s.now()
	}
	s.projects[p.ID] = p
	return p.ID, nil
}

func (s *Store) GetProject(_ context.Context, id int64) (project.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return project.Project{}, ports.ErrNotFound
	}
	return p, nil
}

func (s *Store) ListProjectsByOwners(_ context.Context, owners []string) ([]project.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := toSet(owners)
	out := []project.Project{}
	for _, p := range s.projects {
		if _, ok := want[p.Owner]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) ListAllProjects(_ context.Context) ([]project.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]project.Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) ListProjectsByTenants(_ context.Context, tenantIDs []int64) ([]project.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[int64]struct{}, len(tenantIDs))
	for _, id := range tenantIDs {
		want[id] = struct{}{}
	}
	out := []project.Project{}
	for _, p := range s.projects {
		if p.TenantID == nil {
			continue
		}
		if _, ok := want[*p.TenantID]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) DeleteProject(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[id]; !ok {
		return ports.ErrNotFound
	}
	delete(s.projects, id)
	return nil
}

// --- Scenarios ------------------------------------------------------------------

func (s *Store) CreateScenario(_ context.Context, p scenario.Scenario) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.planSeq++
	p.ID = s.planSeq
	if p.CreatedTime.IsZero() {
		p.CreatedTime = s.now()
	}
	s.scenarios[p.ID] = p
	return p.ID, nil
}

func (s *Store) GetScenario(_ context.Context, id int64) (scenario.Scenario, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.scenarios[id]
	if !ok {
		return scenario.Scenario{}, ports.ErrNotFound
	}
	return p, nil
}

func (s *Store) ListScenariosByProject(_ context.Context, projectID int64) ([]scenario.Scenario, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []scenario.Scenario{}
	for _, p := range s.scenarios {
		if p.ProjectID == projectID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) DeleteScenario(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scenarios[id]; !ok {
		return ports.ErrNotFound
	}
	delete(s.scenarios, id)
	delete(s.planTest, id)
	delete(s.planData, id)
	delete(s.scenarioRequests, id)
	return nil
}

func (s *Store) AddScenarioFile(_ context.Context, scenarioID int64, filename string, isTest bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isTest {
		if _, exists := s.planTest[scenarioID]; exists {
			return ports.ErrFileExists
		}
		s.planTest[scenarioID] = filename
		return nil
	}
	files := s.planData[scenarioID]
	if files == nil {
		files = make(map[string]struct{})
		s.planData[scenarioID] = files
	}
	if _, exists := files[filename]; exists {
		return ports.ErrFileExists
	}
	files[filename] = struct{}{}
	return nil
}

func (s *Store) ScenarioFilesFor(_ context.Context, scenarioID int64) (ports.ScenarioFiles, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf := ports.ScenarioFiles{TestFile: s.planTest[scenarioID], Data: keys(s.planData[scenarioID])}
	return pf, nil
}

func (s *Store) DeleteScenarioFile(_ context.Context, scenarioID int64, filename string, isTest bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isTest {
		if s.planTest[scenarioID] != filename {
			return ports.ErrNotFound
		}
		delete(s.planTest, scenarioID)
		return nil
	}
	files := s.planData[scenarioID]
	if _, ok := files[filename]; !ok {
		return ports.ErrNotFound
	}
	delete(files, filename)
	return nil
}

// SetScenarioKind records how a scenario's workload is expressed.
func (s *Store) SetScenarioKind(_ context.Context, scenarioID int64, kind scenario.Kind, engine taurus.Executor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.scenarios[scenarioID]
	if !ok {
		return ports.ErrNotFound
	}
	sc.Kind = kind
	sc.Engine = engine
	s.scenarios[scenarioID] = sc
	return nil
}

// SetScenarioRequests stores a portable scenario's declarative workload,
// overwriting whatever was stored before.
func (s *Store) SetScenarioRequests(_ context.Context, scenarioID int64, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := make([]byte, len(raw))
	copy(stored, raw)
	s.scenarioRequests[scenarioID] = stored
	return nil
}

// GetScenarioRequests returns a portable scenario's stored fragment, or
// ports.ErrNotFound if nothing has been uploaded yet.
func (s *Store) GetScenarioRequests(_ context.Context, scenarioID int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.scenarioRequests[scenarioID]
	if !ok {
		return nil, ports.ErrNotFound
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

func (s *Store) ScenarioInUse(_ context.Context, scenarioID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, scenarios := range s.exec {
		for _, ep := range scenarios {
			if ep.ScenarioID == scenarioID {
				return true, nil
			}
		}
	}
	return false, nil
}

// --- Executions ------------------------------------------------------------

func (s *Store) CreateExecution(_ context.Context, c execution.Execution) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collSeq++
	c.ID = s.collSeq
	if c.CreatedTime.IsZero() {
		c.CreatedTime = s.now()
	}
	s.executions[c.ID] = c
	return c.ID, nil
}

func (s *Store) GetExecution(_ context.Context, id int64) (execution.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.executions[id]
	if !ok {
		return execution.Execution{}, ports.ErrNotFound
	}
	return c, nil
}

func (s *Store) ListExecutionsByProject(_ context.Context, projectID int64) ([]execution.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []execution.Execution{}
	for _, c := range s.executions {
		if c.ProjectID == projectID {
			out = append(out, c)
		}
	}
	// Map iteration order is random; callers (digests) present executions
	// in a stable order, so mirror the mysql adapter's id ordering.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListExecutionsByProjects returns every execution belonging to any of
// projectIDs, newest first (created time desc, id desc as the tiebreak so the
// order is total and matches MySQL's ORDER BY).
func (s *Store) ListExecutionsByProjects(_ context.Context, projectIDs []int64) ([]execution.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[int64]struct{}, len(projectIDs))
	for _, id := range projectIDs {
		want[id] = struct{}{}
	}
	out := []execution.Execution{}
	for _, c := range s.executions {
		if _, ok := want[c.ProjectID]; ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedTime.Equal(out[j].CreatedTime) {
			return out[i].CreatedTime.After(out[j].CreatedTime)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// ExecutionsWithActiveRunOnCluster returns the ids of executions on cluster
// that have an active run, ordered by id.
func (s *Store) ExecutionsWithActiveRunOnCluster(_ context.Context, cluster string) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for id, exe := range s.executions {
		if exe.Cluster != cluster {
			continue
		}
		if _, ok := s.currentRun[id]; ok {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *Store) DeleteExecution(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.executions[id]; !ok {
		return ports.ErrNotFound
	}
	delete(s.executions, id)
	delete(s.execData, id)
	delete(s.exec, id)
	return nil
}

func (s *Store) AddExecutionFile(_ context.Context, executionID int64, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.execData[executionID]
	if files == nil {
		files = make(map[string]struct{})
		s.execData[executionID] = files
	}
	if _, exists := files[filename]; exists {
		return ports.ErrFileExists
	}
	files[filename] = struct{}{}
	return nil
}

func (s *Store) ExecutionFilesFor(_ context.Context, executionID int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return keys(s.execData[executionID]), nil
}

func (s *Store) DeleteExecutionFile(_ context.Context, executionID int64, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.execData[executionID]
	if _, ok := files[filename]; !ok {
		return ports.ErrNotFound
	}
	delete(files, filename)
	return nil
}

func (s *Store) StoreLoadProfile(_ context.Context, executionID int64, csvSplit bool, scenarios []loadprofile.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.executions[executionID]
	if !ok {
		return ports.ErrNotFound
	}
	// The persisted schema (execution_scenario) does not store the scenario name, so
	// the fake drops it too to stay behaviourally identical to MySQL.
	stored := make([]loadprofile.Entry, len(scenarios))
	for i, ep := range scenarios {
		ep.Name = ""
		stored[i] = ep
	}
	s.exec[executionID] = stored
	c.CSVSplit = csvSplit
	s.executions[executionID] = c
	return nil
}

func (s *Store) LoadProfileFor(_ context.Context, executionID int64) ([]loadprofile.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]loadprofile.Entry(nil), s.exec[executionID]...), nil
}

// SetExecutionCriteria replaces the execution's configured Taurus pass/fail
// criteria with criteria, in the given order.
func (s *Store) SetExecutionCriteria(_ context.Context, executionID int64, criteria []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execCriteria[executionID] = append([]string(nil), criteria...)
	return nil
}

// CriteriaFor returns the execution's currently configured criteria, in the
// order they were set. Never nil.
func (s *Store) CriteriaFor(_ context.Context, executionID int64) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.execCriteria[executionID]...), nil
}

// SetPendingCorrelationID records the trace id a Deploy minted for the run it
// precedes, overwriting any earlier one (last deploy wins).
func (s *Store) SetPendingCorrelationID(_ context.Context, executionID int64, correlationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.executions[executionID]; !ok {
		return ports.ErrNotFound
	}
	s.pendingCorrelation[executionID] = correlationID
	return nil
}

// PendingCorrelationID returns the id the latest Deploy minted (” when none).
func (s *Store) PendingCorrelationID(_ context.Context, executionID int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.executions[executionID]; !ok {
		return "", ports.ErrNotFound
	}
	return s.pendingCorrelation[executionID], nil
}

// TouchActivity stamps the execution's last-activity clock to now. An
// unknown execution id is a dropped stamp, not an error -- matching the mysql
// adapter, where affected rows are deliberately not checked (NOW()'s second
// precision would mistake a same-second second touch for a missing row).
func (s *Store) TouchActivity(_ context.Context, executionID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity[executionID] = s.now()
	s.touchCount[executionID]++
	return nil
}

// LastActivity returns the execution's last-activity stamp; ok is false for
// no row or a never-stamped one, mirroring the mysql adapter's NULL/no-row
// collapse (either way the caller falls back to another clock).
func (s *Store) LastActivity(_ context.Context, executionID int64) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.executions[executionID]; !ok {
		return time.Time{}, false, nil
	}
	last, ok := s.lastActivity[executionID]
	return last, ok, nil
}

// TouchActivityCount reports how many times the execution's idle clock was
// stamped -- a test hook distinguishing WHICH lifecycle path stamped the
// clock (deploy, trigger, teardown) when their stamps land in the same
// second and the timestamps alone cannot.
func (s *Store) TouchActivityCount(executionID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.touchCount[executionID]
}

// StoreExecutionConfig replaces the execution's load profile and configured
// criteria together, under a single lock -- mirrors the mysql adapter's
// single-transaction guarantee that the two never observably desync.
func (s *Store) StoreExecutionConfig(_ context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry, criteria []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.executions[executionID]
	if !ok {
		return ports.ErrNotFound
	}
	// The persisted schema (execution_scenario) does not store the scenario name, so
	// the fake drops it too to stay behaviourally identical to MySQL.
	stored := make([]loadprofile.Entry, len(entries))
	for i, ep := range entries {
		ep.Name = ""
		stored[i] = ep
	}
	s.exec[executionID] = stored
	c.CSVSplit = csvSplit
	s.executions[executionID] = c
	s.execCriteria[executionID] = append([]string(nil), criteria...)
	return nil
}

// --- helpers ----------------------------------------------------------------

func toSet(ss []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		set[s] = struct{}{}
	}
	return set
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
