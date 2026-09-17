// Package scenarioapp implements the application use-cases for scenarios and their
// files (a JMX test file plus data files). It coordinates the scenario repository
// and the object store; the storage key convention is "scenario/{id}/{filename}".
package scenarioapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/domain/account"
	"github.com/heridotlife/honryu/internal/domain/jmx"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Business-rule errors. Callers compare with errors.Is.
var (
	ErrScenarioInUse       = errors.New("scenarioapp: scenario is in use by an execution")
	ErrInvalidFilename     = errors.New("scenarioapp: invalid filename")
	ErrRequestsInvalid     = errors.New("scenarioapp: requests fragment is invalid")
	ErrScenarioNotPortable = errors.New("scenarioapp: scenario is not portable")
	// ErrScenarioNotTemplate refuses instantiate on an ordinary scenario: the
	// instantiate route addresses templates, and silently cloning a runnable
	// scenario through it would hide a wrong-id mistake behind a success.
	ErrScenarioNotTemplate = errors.New("scenarioapp: scenario is not a template")
	// ErrVersioningUnavailable says the service was wired without a version
	// store, so history cannot be served or restored to. Production wires
	// one (WithVersions); the error exists so the HTTP layer can answer the
	// version endpoints with its usual not-configured 404 instead of a 500.
	ErrVersioningUnavailable = errors.New("scenarioapp: scenario versioning is not configured")
)

// Repo is the repository surface the scenario service needs.
type Repo interface {
	CreateScenario(ctx context.Context, p scenario.Scenario) (int64, error)
	GetScenario(ctx context.Context, id int64) (scenario.Scenario, error)
	ListScenariosByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error)
	DeleteScenario(ctx context.Context, id int64) error
	AddScenarioFile(ctx context.Context, scenarioID int64, filename string, isTest bool) error
	ScenarioFilesFor(ctx context.Context, scenarioID int64) (ports.ScenarioFiles, error)
	DeleteScenarioFile(ctx context.Context, scenarioID int64, filename string, isTest bool) error
	ScenarioInUse(ctx context.Context, scenarioID int64) (bool, error)
	SetScenarioKind(ctx context.Context, scenarioID int64, kind scenario.Kind, engine taurus.Executor) error
	SetScenarioRequests(ctx context.Context, scenarioID int64, raw []byte) error
	// GetScenarioRequests backs ScenarioFingerprint's read of a portable
	// scenario's declarative workload. ErrNotFound means none was ever
	// uploaded -- not an error for a fingerprint, just an empty contribution.
	GetScenarioRequests(ctx context.Context, scenarioID int64) ([]byte, error)
	// GetProject resolves the owning project's tenant so Create can stamp it
	// onto the scenario (Phase 20 tenant propagation).
	GetProject(ctx context.Context, id int64) (project.Project, error)

	// UpdateScenario and DeleteScenarioRequests are the restore path's
	// apply steps: the row's mutable fields rewritten from the snapshot,
	// and the reverse of SetScenarioRequests for rewinding to a version
	// captured before any fragment existed.
	UpdateScenario(ctx context.Context, p scenario.Scenario) error
	DeleteScenarioRequests(ctx context.Context, scenarioID int64) error

	// ListTemplates returns the template catalog (every scenario flagged as a
	// template), in id order. Templates are global, so there is nothing to
	// scope by.
	ListTemplates(ctx context.Context) ([]scenario.Scenario, error)
}

// Service provides scenario use-cases.
type Service struct {
	repo  Repo
	store ports.ObjectStore
	// versions records the edit audit trail when wired (WithVersions).
	// Production wires it; a service without one still serves every
	// pre-versioning use-case, it just does not version (the legacy unit
	// harnesses rely on that) -- and its version endpoints report
	// ErrVersioningUnavailable.
	versions ports.ScenarioVersionStore
}

// NewService wires a Service to a scenario repository and an object store.
func NewService(repo Repo, store ports.ObjectStore) *Service {
	return &Service{repo: repo, store: store}
}

// WithVersions wires the scenario-version store backing the edit audit
// trail. Returns the receiver for chaining.
func (s *Service) WithVersions(vs ports.ScenarioVersionStore) *Service {
	if vs != nil {
		s.versions = vs
	}
	return s
}

// FileRef describes a stored file and how to fetch it.
type FileRef struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// Files lists a scenario's files with retrieval URLs.
type Files struct {
	TestFile *FileRef  `json:"test_file"`
	Data     []FileRef `json:"data"`
}

// Create validates input and persists a new scenario.
func (s *Service) Create(ctx context.Context, name string, projectID int64) (scenario.Scenario, error) {
	p, err := scenario.New(name, projectID)
	if err != nil {
		return scenario.Scenario{}, err
	}
	tenantID, err := s.resolveTenant(ctx, projectID)
	if err != nil {
		return scenario.Scenario{}, err
	}
	p.TenantID = tenantID
	id, err := s.repo.CreateScenario(ctx, p)
	if err != nil {
		return scenario.Scenario{}, err
	}
	p.ID = id
	// Phase 80: creation captures version 1 -- the starting point every
	// later edit is measured from. The capture needs the stored row (the
	// store assigns created_time); on failure the row is rolled back so a
	// scenario is never left behind without its first version, the same
	// stance ImportJMX and Instantiate take for their later steps.
	if s.versions != nil {
		stored, getErr := s.repo.GetScenario(ctx, id)
		if getErr != nil {
			_ = s.repo.DeleteScenario(ctx, id)
			return scenario.Scenario{}, getErr
		}
		if _, captureErr := s.captureVersionOf(ctx, stored); captureErr != nil {
			_ = s.repo.DeleteScenario(ctx, id)
			return scenario.Scenario{}, captureErr
		}
	}
	return p, nil
}

// Get returns a scenario by ID (ports.ErrNotFound if absent).
func (s *Service) Get(ctx context.Context, id int64) (scenario.Scenario, error) {
	return s.repo.GetScenario(ctx, id)
}

// ListByProject returns the scenarios belonging to a project, minus templates:
// templates are global starting points, not project tests, so every caller that
// lists a project's scenarios gets only runnable ones. (The adapter-level list
// is a dumb lens and would include them; the exclusion is enforced here, once,
// for every consumer of the service.)
func (s *Service) ListByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error) {
	all, err := s.repo.ListScenariosByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]scenario.Scenario, 0, len(all))
	for _, p := range all {
		if !p.IsTemplate {
			out = append(out, p)
		}
	}
	return out, nil
}

// Templates lists the template catalog, in id order. Templates are global --
// no project, no tenant -- so there is nothing to scope by.
func (s *Service) Templates(ctx context.Context) ([]scenario.Scenario, error) {
	return s.repo.ListTemplates(ctx)
}

// InstantiateOverrides is the tiny, documented surface a caller may change
// when instantiating: everything else about the template is cloned verbatim.
type InstantiateOverrides struct {
	// TargetURL replaces the fragment's default-address (the base URL every
	// relative request URL resolves against). Empty means keep the
	// template's. Normalized the way the NewTest flow normalizes its own
	// target: surrounding space trimmed, one trailing slash dropped.
	TargetURL string
}

// InstantiateInput is an Instantiate request: which template, the new
// scenario's name and owning project, and the overrides.
type InstantiateInput struct {
	// Name for the new scenario (validated exactly like Service.Create's).
	Name string
	// ProjectID the clone belongs to (a template itself belongs to none).
	ProjectID int64
	// Overrides, all optional.
	Overrides InstantiateOverrides
}

// Instantiate clones a template into a fresh, ordinary scenario in projectID
// and returns it (ID assigned). The clone is portable and carries the
// template's requests fragment; overrides are applied to the fragment before
// it is stored.
//
// The fragment is stored through SetRequests -- the one validate-and-store
// path -- so a template can never produce a clone SetRequests would have
// rejected, and no validation logic is duplicated here. A failure after the
// scenario row was created rolls the row back, so a failed instantiate leaves
// nothing behind (the same stance ImportJMX takes). Files are deliberately
// not cloned: the seeded templates carry none, and a template with files
// would need an override surface this phase does not have.
func (s *Service) Instantiate(ctx context.Context, templateID int64, in InstantiateInput) (scenario.Scenario, error) {
	tpl, err := s.repo.GetScenario(ctx, templateID)
	if err != nil {
		return scenario.Scenario{}, err
	}
	if !tpl.IsTemplate {
		return scenario.Scenario{}, fmt.Errorf("%w: scenario %d is not a template", ErrScenarioNotTemplate, templateID)
	}
	if tpl.Kind != scenario.KindPortable {
		return scenario.Scenario{}, fmt.Errorf("%w: template %d is %s", ErrScenarioNotPortable, templateID, tpl.Kind)
	}
	raw, err := s.repo.GetScenarioRequests(ctx, templateID)
	if err != nil {
		return scenario.Scenario{}, err
	}

	if in.Overrides.TargetURL != "" {
		raw, err = withDefaultAddress(raw, in.Overrides.TargetURL)
		if err != nil {
			return scenario.Scenario{}, err
		}
	}

	sc, err := scenario.New(in.Name, in.ProjectID)
	if err != nil {
		return scenario.Scenario{}, err
	}
	tenantID, tenantErr := s.resolveTenant(ctx, in.ProjectID)
	if tenantErr != nil {
		return scenario.Scenario{}, tenantErr
	}
	sc.TenantID = tenantID
	id, err := s.repo.CreateScenario(ctx, sc)
	if err != nil {
		return scenario.Scenario{}, err
	}
	sc.ID = id

	if err := s.SetRequests(ctx, id, raw); err != nil {
		// Roll back, so a failed instantiate leaves no scenario behind.
		_ = s.repo.DeleteScenario(ctx, id)
		return scenario.Scenario{}, err
	}
	return sc, nil
}

// withDefaultAddress rewrites a fragment's default-address. The fragment is
// parsed as the same taurus.Scenario SetRequests validates into; a fragment
// this function cannot parse is a stored-template inconsistency (it was
// validated when stored), reported as the InvalidRequestsError the editor
// paths already speak. The URL is normalized the way the NewTest flow
// normalizes its own target field, so an override and a hand-typed test
// produce the same fragment.
func withDefaultAddress(raw []byte, targetURL string) ([]byte, error) {
	var frag taurus.Scenario
	if err := yaml.Unmarshal(raw, &frag); err != nil {
		return nil, &InvalidRequestsError{Diagnostics: diagnosticsFromYAMLError(err), Err: ErrRequestsInvalid}
	}
	frag.DefaultAddress = strings.TrimRight(strings.TrimSpace(targetURL), "/")
	out, err := yaml.Marshal(&frag)
	if err != nil {
		return nil, fmt.Errorf("scenarioapp: re-marshal fragment: %w", err)
	}
	return out, nil
}

// Delete removes a scenario (and its files) unless it is used by an execution.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.repo.GetScenario(ctx, id); err != nil {
		return err
	}
	inUse, err := s.repo.ScenarioInUse(ctx, id)
	if err != nil {
		return err
	}
	if inUse {
		return ErrScenarioInUse
	}
	files, err := s.repo.ScenarioFilesFor(ctx, id)
	if err != nil {
		return err
	}
	for _, name := range allFileNames(files) {
		if delErr := s.store.Delete(ctx, scenarioKey(id, name)); delErr != nil {
			return delErr
		}
	}
	return s.repo.DeleteScenario(ctx, id)
}

// ImportResult is a completed JMX import: the scenario that now exists, and
// what the inspector found in the plan.
type ImportResult struct {
	Scenario scenario.Scenario `json:"scenario"`
	Report   jmx.Report        `json:"report"`
}

// ImportJMX creates a scenario from a Shibuya JMeter plan.
//
// The plan is not converted: Taurus runs a .jmx directly, so the scenario is
// native and pinned to JMeter. What the import adds is the inspector's report --
// the plan's own load settings that Honryu overrides, data files the pod must be
// given, and listeners that will not do what their author expects. The file is
// stored only after it has been inspected, so an unusable plan never becomes a
// scenario.
func (s *Service) ImportJMX(ctx context.Context, name string, projectID int64, filename string, content io.Reader) (ImportResult, error) {
	if err := validateFilename(filename); err != nil {
		return ImportResult{}, err
	}
	if !isJMX(filename) {
		return ImportResult{}, fmt.Errorf("%w: %q is not a .jmx file", ErrInvalidFilename, filename)
	}

	raw, err := io.ReadAll(content)
	if err != nil {
		return ImportResult{}, fmt.Errorf("scenarioapp: read plan: %w", err)
	}
	report, err := jmx.Inspect(bytes.NewReader(raw))
	if err != nil {
		return ImportResult{}, err
	}

	sc, err := scenario.NewNative(strings.TrimSpace(name), projectID, taurus.ExecutorJMeter)
	if err != nil {
		return ImportResult{}, err
	}
	tenantID, err := s.resolveTenant(ctx, projectID)
	if err != nil {
		return ImportResult{}, err
	}
	sc.TenantID = tenantID
	id, err := s.repo.CreateScenario(ctx, sc)
	if err != nil {
		return ImportResult{}, err
	}
	sc.ID = id

	if err := s.UploadFile(ctx, id, filename, bytes.NewReader(raw)); err != nil {
		// Roll back, so a failed import leaves no scenario behind.
		_ = s.repo.DeleteScenario(ctx, id)
		return ImportResult{}, err
	}
	return ImportResult{Scenario: sc, Report: report}, nil
}

// Files returns the scenario's files with URLs.
func (s *Service) Files(ctx context.Context, scenarioID int64) (Files, error) {
	pf, err := s.repo.ScenarioFilesFor(ctx, scenarioID)
	if err != nil {
		return Files{}, err
	}
	out := Files{Data: make([]FileRef, 0, len(pf.Data))}
	if pf.TestFile != "" {
		out.TestFile = &FileRef{Filename: pf.TestFile, URL: s.store.URL(scenarioKey(scenarioID, pf.TestFile))}
	}
	for _, name := range pf.Data {
		out.Data = append(out.Data, FileRef{Filename: name, URL: s.store.URL(scenarioKey(scenarioID, name))})
	}
	return out, nil
}

// UploadFile records and stores a scenario file. A ".jmx" file is stored as the
// scenario's single test file; anything else is a data file. Returns
// ports.ErrFileExists if the file is already present.
func (s *Service) UploadFile(ctx context.Context, scenarioID int64, filename string, content io.Reader) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	if _, err := s.repo.GetScenario(ctx, scenarioID); err != nil {
		return err
	}
	isTest := isTestFile(filename)
	// A duplicate is refused before anything is captured: a rejected upload
	// changed nothing, and a version row for a no-op would be noise. (The
	// store's own ErrFileExists below remains as the race backstop.)
	pf, err := s.repo.ScenarioFilesFor(ctx, scenarioID)
	if err != nil {
		return err
	}
	if filePresent(pf, filename, isTest) {
		return ports.ErrFileExists
	}
	// Phase 80: capture the file set BEFORE the new record lands, so the
	// audit trail shows the state the upload changed. A failed capture
	// fails the upload -- an audit trail that silently drops entries is a
	// lie -- and the capture runs before any mutation, so nothing is
	// half-done.
	if err := s.captureVersion(ctx, scenarioID); err != nil {
		return err
	}
	if err := s.repo.AddScenarioFile(ctx, scenarioID, filename, isTest); err != nil {
		return err
	}
	// An engine-native artefact decides what the scenario is: a .jmx only runs
	// on JMeter, a .js only on k6. Without this a scenario stays portable with no
	// requests, which nothing can compile -- so uploading a script would appear
	// to work and then fail at deploy.
	if isTest {
		if engine, ok := engineForArtefact(filename); ok {
			if err := s.repo.SetScenarioKind(ctx, scenarioID, scenario.KindNative, engine); err != nil {
				return err
			}
		}
	}
	if err := s.store.Upload(ctx, scenarioKey(scenarioID, filename), content); err != nil {
		// Roll back the record so it does not dangle without an object.
		_ = s.repo.DeleteScenarioFile(ctx, scenarioID, filename, isTest)
		return err
	}
	return nil
}

// DownloadFile returns the bytes of a scenario file (ports.ErrObjectNotFound if
// absent).
func (s *Service) DownloadFile(ctx context.Context, scenarioID int64, filename string) ([]byte, error) {
	if err := validateFilename(filename); err != nil {
		return nil, err
	}
	return s.store.Download(ctx, scenarioKey(scenarioID, filename))
}

// DeleteFile removes a scenario file record and its stored object.
func (s *Service) DeleteFile(ctx context.Context, scenarioID int64, filename string) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	isTest := isTestFile(filename)
	// A file that is not there is refused before anything is captured -- a
	// no-op delete must not leave a version row behind (the same rule
	// UploadFile applies to duplicates).
	pf, err := s.repo.ScenarioFilesFor(ctx, scenarioID)
	if err != nil {
		return err
	}
	if !filePresent(pf, filename, isTest) {
		return ports.ErrNotFound
	}
	// Phase 80: capture before the record goes -- same law as UploadFile.
	if err := s.captureVersion(ctx, scenarioID); err != nil {
		return err
	}
	if err := s.repo.DeleteScenarioFile(ctx, scenarioID, filename, isTest); err != nil {
		return err
	}
	return s.store.Delete(ctx, scenarioKey(scenarioID, filename))
}

// SetRequests stores a portable scenario's declarative workload from raw,
// the bytes of a Taurus `scenarios:` fragment (the same shape as one entry
// of Config.Scenarios) -- validated here so a malformed or empty upload is
// rejected with a clear reason at upload time, rather than only surfacing as
// compile.ErrRequestsRequired the next time the scenario is deployed.
//
// Rejected for a native scenario: compileShards never reads a native
// scenario's stored requests (it runs the uploaded script instead), so
// accepting the upload here would silently store data nothing will ever use
// -- a success response indistinguishable from a meaningful one.
//
// raw is stored exactly as uploaded, not the re-marshaled struct: a caller's
// YAML formatting, comments, and key order survive untouched.
//
// Validation is the one path in requestDiagnostics, shared with
// ValidateRequests so validate and store cannot disagree. A rejected
// fragment returns *InvalidRequestsError (wrapping ErrRequestsInvalid)
// carrying line-anchored diagnostics; errors.Is against the sentinels is
// unchanged for every existing caller.
func (s *Service) SetRequests(ctx context.Context, scenarioID int64, raw []byte) error {
	sc, err := s.repo.GetScenario(ctx, scenarioID)
	if err != nil {
		return err
	}
	if sc.Kind != scenario.KindPortable {
		return fmt.Errorf("%w: scenario %d is %s", ErrScenarioNotPortable, scenarioID, sc.Kind)
	}
	if diags := requestDiagnostics(raw); diags != nil {
		return &InvalidRequestsError{Diagnostics: diags, Err: ErrRequestsInvalid}
	}
	// Phase 80: capture before the fragment is overwritten -- the edit the
	// editor's Save triggers is exactly what the audit trail exists for.
	if err := s.captureVersion(ctx, scenarioID); err != nil {
		return err
	}
	return s.repo.SetScenarioRequests(ctx, scenarioID, raw)
}

// ValidateRequests validates a fragment without storing it: the same checks
// SetRequests runs, with the same accept/reject verdict. Scenario existence
// and kind are checked first, exactly as SetRequests checks them; a fragment
// this method accepts is one SetRequests would store, and a fragment it
// rejects comes back as the same *InvalidRequestsError SetRequests returns,
// so validate and store cannot disagree. The handler turns the error into
// the 400 diagnostics envelope; a nil error means the fragment would store.
func (s *Service) ValidateRequests(ctx context.Context, scenarioID int64, raw []byte) ([]Diagnostic, error) {
	sc, err := s.repo.GetScenario(ctx, scenarioID)
	if err != nil {
		return nil, err
	}
	if sc.Kind != scenario.KindPortable {
		return nil, fmt.Errorf("%w: scenario %d is %s", ErrScenarioNotPortable, scenarioID, sc.Kind)
	}
	if diags := requestDiagnostics(raw); diags != nil {
		return nil, &InvalidRequestsError{Diagnostics: diags, Err: ErrRequestsInvalid}
	}
	// G6: informational findings ride the 200 -- keys taurus.Scenario does
	// not model are stored exactly as uploaded but never reach the engine.
	// They never block storing and never appear on the store path.
	return uncompiledKeyDiagnostics(raw), nil
}

// Requests returns a portable scenario's stored requests fragment exactly as
// it was uploaded -- byte-for-byte, comments and key order included. It is
// the load half of the editor round trip; SetRequests is the save half.
// ports.ErrNotFound means nothing was ever uploaded. A native scenario is
// ErrScenarioNotPortable -- the same stance SetRequests takes, so load and
// save cannot disagree about what a scenario kind accepts.
func (s *Service) Requests(ctx context.Context, scenarioID int64) ([]byte, error) {
	// Scenario existence and kind are checked first: a native scenario must
	// 409 (not 404) even though it also has no fragment, and an unknown
	// scenario must 404 rather than leak whether a fragment exists.
	sc, err := s.repo.GetScenario(ctx, scenarioID)
	if err != nil {
		return nil, err
	}
	if sc.Kind != scenario.KindPortable {
		return nil, fmt.Errorf("%w: scenario %d is %s", ErrScenarioNotPortable, scenarioID, sc.Kind)
	}
	return s.repo.GetScenarioRequests(ctx, scenarioID)
}

// ScenarioFingerprint returns a deterministic hash over a scenario's actual
// content: its uploaded files (test file plus data, sorted by filename) and
// its declarative requests fragment, if any. Identical content -- including
// a byte-identical re-upload -- hashes identically; any real change (a new,
// changed, or removed file, or an edited requests fragment) hashes
// differently.
//
// This is what a CapacityProfile's own ScenarioFingerprint is checked
// against for staleness (capacityprofile.FanOut): a calibration must never
// be presented as still valid once the scenario it measured has actually
// changed, and a false staleness (recalibrating unnecessarily) is the only
// acceptable failure mode, never a false freshness.
func (s *Service) ScenarioFingerprint(ctx context.Context, scenarioID int64) (string, error) {
	// ScenarioFilesFor never errors, even for an unknown id -- it just
	// returns no files. Without this check, a deleted scenario would
	// silently fingerprint as "empty" rather than erroring, and an empty
	// fingerprint could accidentally match a profile calibrated against a
	// scenario that likewise had no files/requests yet -- a false-freshness
	// bug, the one staleness must never produce.
	if _, err := s.repo.GetScenario(ctx, scenarioID); err != nil {
		return "", err
	}
	files, err := s.repo.ScenarioFilesFor(ctx, scenarioID)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(files.Data)+1)
	if files.TestFile != "" {
		names = append(names, files.TestFile)
	}
	names = append(names, files.Data...)
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		content, err := s.store.Download(ctx, scenarioKey(scenarioID, name))
		if err != nil {
			return "", err
		}
		// Each segment is length-prefixed so no concatenation of
		// differently-split file contents can collide with another.
		// hash.Hash.Write/Fprintf into it never actually errors.
		_, _ = fmt.Fprintf(h, "file:%s:%d:", name, len(content))
		h.Write(content)
	}

	requests, err := s.repo.GetScenarioRequests(ctx, scenarioID)
	if err != nil && !errors.Is(err, ports.ErrNotFound) {
		return "", err
	}
	_, _ = fmt.Fprintf(h, "requests:%d:", len(requests))
	h.Write(requests)

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Version history (phase 80). The audit trail lives behind the
// ScenarioVersionStore: every write the service applies captures the
// CURRENT state as version max+1 first, creation captures version 1, and
// restore captures the pre-restore state as a new version -- history is
// append-only, no path rewrites a stored row.

// VersionsEnabled reports whether the service was wired with a version
// store. The HTTP layer gates the version endpoints on it (not configured
// 404s, the optional-service contract).
func (s *Service) VersionsEnabled() bool { return s.versions != nil }

// ListVersions returns the scenario's versions, newest first. Unknown
// scenario is ports.ErrNotFound; an unwired store is
// ErrVersioningUnavailable.
func (s *Service) ListVersions(ctx context.Context, scenarioID int64) ([]ports.ScenarioVersionMeta, error) {
	if _, err := s.repo.GetScenario(ctx, scenarioID); err != nil {
		return nil, err
	}
	if s.versions == nil {
		return nil, ErrVersioningUnavailable
	}
	return s.versions.ListScenarioVersions(ctx, scenarioID)
}

// ScenarioVersion returns one stored version, snapshot included. Unknown
// scenario and unknown version map to their own sentinels so the HTTP
// layer's 404 can name which one.
func (s *Service) ScenarioVersion(ctx context.Context, scenarioID int64, version int) (ports.ScenarioVersion, error) {
	if _, err := s.repo.GetScenario(ctx, scenarioID); err != nil {
		return ports.ScenarioVersion{}, err
	}
	if s.versions == nil {
		return ports.ScenarioVersion{}, ErrVersioningUnavailable
	}
	return s.versions.ScenarioVersion(ctx, scenarioID, version)
}

// RestoreResult says what a restore did: which version's snapshot was
// applied, and which version now records the pre-restore state (the one
// the restore itself created -- restore is itself a version).
type RestoreResult struct {
	// RestoredFrom is the version whose snapshot was applied.
	RestoredFrom int
	// Version is the freshly appended capture of the pre-restore state.
	Version int
}

// Restore rewinds the scenario to version: that version's snapshot becomes
// the live scenario, after the current state is captured as a new version
// (append-only -- no stored row is ever rewritten or deleted).
//
// The snapshot records file NAMES, not blobs: restoring reconciles the
// file records to the snapshot's set, so a record restored after its
// object was deleted downloads as not-found until re-uploaded. Ownership
// (project, tenant) and provenance (created_by, created_time) are never
// restored -- a restore rewinds what the scenario IS, not what it belongs
// to. Returns ErrVersioningUnavailable when unwired, ports.ErrNotFound for
// an unknown scenario, ports.ErrScenarioVersionNotFound for an unknown
// version.
func (s *Service) Restore(ctx context.Context, scenarioID int64, version int) (RestoreResult, error) {
	sc, err := s.repo.GetScenario(ctx, scenarioID)
	if err != nil {
		return RestoreResult{}, err
	}
	if s.versions == nil {
		return RestoreResult{}, ErrVersioningUnavailable
	}
	stored, err := s.versions.ScenarioVersion(ctx, scenarioID, version)
	if err != nil {
		return RestoreResult{}, err
	}

	// The append-only law, restore included: the pre-restore state becomes
	// version max+1 BEFORE the snapshot is applied, so the operator can
	// always undo a restore by restoring the version this call created.
	preRestore, err := s.captureVersionOf(ctx, sc)
	if err != nil {
		return RestoreResult{}, err
	}

	snap := stored.Snapshot
	rewound := scenario.Scenario{
		ID:   scenarioID,
		Name: snap.Name,
		// The snapshot's own project_id only satisfies the domain's
		// validation; UpdateScenario never writes ownership columns.
		ProjectID:    snap.ProjectID,
		Kind:         scenario.Kind(snap.Kind),
		Engine:       taurus.Executor(snap.Engine),
		IsTemplate:   snap.IsTemplate,
		TemplateName: snap.TemplateName,
	}
	if err := rewound.Validate(); err != nil {
		return RestoreResult{}, fmt.Errorf("scenarioapp: restore scenario %d to version %d: %w", scenarioID, version, err)
	}
	if err := s.repo.UpdateScenario(ctx, rewound); err != nil {
		return RestoreResult{}, err
	}
	if err := s.restoreFiles(ctx, scenarioID, snap); err != nil {
		return RestoreResult{}, err
	}
	if snap.Requests == "" {
		err = s.repo.DeleteScenarioRequests(ctx, scenarioID)
	} else {
		err = s.repo.SetScenarioRequests(ctx, scenarioID, []byte(snap.Requests))
	}
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{RestoredFrom: version, Version: preRestore}, nil
}

// restoreFiles reconciles the scenario's file records to the snapshot's
// set: gone records are re-added, records not in the snapshot are removed.
// Stored objects are deliberately untouched -- the snapshot has names, not
// contents, so this is a metadata rewind only.
func (s *Service) restoreFiles(ctx context.Context, scenarioID int64, snap ports.ScenarioSnapshot) error {
	current, err := s.repo.ScenarioFilesFor(ctx, scenarioID)
	if err != nil {
		return err
	}
	if current.TestFile != snap.TestFile {
		if current.TestFile != "" {
			if err := s.repo.DeleteScenarioFile(ctx, scenarioID, current.TestFile, true); err != nil {
				return err
			}
		}
		if snap.TestFile != "" {
			if err := s.repo.AddScenarioFile(ctx, scenarioID, snap.TestFile, true); err != nil {
				return err
			}
		}
	}
	wanted := make(map[string]bool, len(snap.Data))
	for _, name := range snap.Data {
		wanted[name] = true
	}
	for _, name := range current.Data {
		if !wanted[name] {
			if err := s.repo.DeleteScenarioFile(ctx, scenarioID, name, false); err != nil {
				return err
			}
		}
	}
	have := make(map[string]bool, len(current.Data))
	for _, name := range current.Data {
		have[name] = true
	}
	for _, name := range snap.Data {
		if !have[name] {
			if err := s.repo.AddScenarioFile(ctx, scenarioID, name, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// captureVersion records the scenario's current state as its next version.
// It reads the scenario first because the caller's copy may predate the
// store's own stamps; a versioning failure fails the caller's write (see
// captureVersionOf).
func (s *Service) captureVersion(ctx context.Context, scenarioID int64) error {
	if s.versions == nil {
		return nil
	}
	sc, err := s.repo.GetScenario(ctx, scenarioID)
	if err != nil {
		return err
	}
	_, err = s.captureVersionOf(ctx, sc)
	return err
}

// captureVersionOf appends sc's state as the scenario's next version and
// returns the number assigned. A no-actor append (the request carried no
// authenticated principal -- the legacy no-auth path) is stored NULL, the
// honest "unknown", never a fabricated name.
func (s *Service) captureVersionOf(ctx context.Context, sc scenario.Scenario) (int, error) {
	snap, err := s.scenarioSnapshot(ctx, sc)
	if err != nil {
		return 0, fmt.Errorf("scenarioapp: snapshot scenario %d: %w", sc.ID, err)
	}
	version, err := s.versions.AppendScenarioVersion(ctx, sc.ID, snap, actorOf(ctx))
	if err != nil {
		return 0, fmt.Errorf("scenarioapp: capture scenario %d version: %w", sc.ID, err)
	}
	return version, nil
}

// scenarioSnapshot composes the full scenario shape from its row, its file
// records, and its stored fragment (none when never uploaded -- the one
// not-found this read tolerates).
func (s *Service) scenarioSnapshot(ctx context.Context, sc scenario.Scenario) (ports.ScenarioSnapshot, error) {
	files, err := s.repo.ScenarioFilesFor(ctx, sc.ID)
	if err != nil {
		return ports.ScenarioSnapshot{}, err
	}
	raw, err := s.repo.GetScenarioRequests(ctx, sc.ID)
	if err != nil && !errors.Is(err, ports.ErrNotFound) {
		return ports.ScenarioSnapshot{}, err
	}
	data := append([]string(nil), files.Data...)
	if data == nil {
		data = []string{}
	}
	return ports.ScenarioSnapshot{
		ID:           sc.ID,
		Name:         sc.Name,
		ProjectID:    sc.ProjectID,
		Kind:         string(sc.Kind),
		Engine:       string(sc.Engine),
		TenantID:     sc.TenantID,
		CreatedBy:    sc.CreatedBy,
		UpdatedBy:    sc.UpdatedBy,
		CreatedTime:  sc.CreatedTime,
		IsTemplate:   sc.IsTemplate,
		TemplateName: sc.TemplateName,
		TestFile:     files.TestFile,
		Data:         data,
		Requests:     string(raw),
	}, nil
}

// actorOf names the authenticated principal the request carried, or ""
// when none was reachable (no-auth mode stores NULL).
func actorOf(ctx context.Context) string {
	return account.FromContext(ctx).Subject
}

// filePresent reports whether filename is already one of pf's records in
// the given slot -- the duplicate/missing pre-check that keeps rejected
// uploads and no-op deletes from recording versions.
func filePresent(pf ports.ScenarioFiles, filename string, isTest bool) bool {
	if isTest {
		return pf.TestFile == filename
	}
	for _, name := range pf.Data {
		if name == filename {
			return true
		}
	}
	return false
}

// resolveTenant returns the tenant a new scenario under projectID should be
// stamped with: its project's tenant (Phase 20 -- a scenario's tenant always
// equals its project's). A missing project is tolerated as nil rather than
// failing the caller: the HTTP layer already 404s on an unknown project
// before reaching either Create or ImportJMX (authorizeProject fetches it
// first), so ErrNotFound here means only a test or future caller that
// skipped that check, and it should get the same untenanted scenario this
// package always produced, not a new failure mode.
func (s *Service) resolveTenant(ctx context.Context, projectID int64) (*int64, error) {
	p, err := s.repo.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return p.TenantID, nil
}

func scenarioKey(scenarioID int64, filename string) string {
	return fmt.Sprintf("scenario/%d/%s", scenarioID, filename)
}

// isTestFile reports whether a filename is a scenario's script rather than one
// of its data files.
func isTestFile(filename string) bool {
	_, ok := engineForArtefact(filename)
	return ok
}

// engineForArtefact maps a script's extension onto the engine that runs it.
func engineForArtefact(filename string) (taurus.Executor, bool) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jmx":
		return taurus.ExecutorJMeter, true
	case ".js":
		return taurus.ExecutorK6, true
	default:
		return "", false
	}
}

func isJMX(filename string) bool {
	return strings.HasSuffix(strings.ToLower(filename), ".jmx")
}

func validateFilename(filename string) error {
	if filename == "" || strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." {
		return ErrInvalidFilename
	}
	return nil
}

func allFileNames(pf ports.ScenarioFiles) []string {
	names := append([]string(nil), pf.Data...)
	if pf.TestFile != "" {
		names = append(names, pf.TestFile)
	}
	return names
}
