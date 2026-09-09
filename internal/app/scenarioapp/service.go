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
}

// Service provides scenario use-cases.
type Service struct {
	repo  Repo
	store ports.ObjectStore
}

// NewService wires a Service to a scenario repository and an object store.
func NewService(repo Repo, store ports.ObjectStore) *Service {
	return &Service{repo: repo, store: store}
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
	return p, nil
}

// Get returns a scenario by ID (ports.ErrNotFound if absent).
func (s *Service) Get(ctx context.Context, id int64) (scenario.Scenario, error) {
	return s.repo.GetScenario(ctx, id)
}

// ListByProject returns the scenarios belonging to a project.
func (s *Service) ListByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error) {
	return s.repo.ListScenariosByProject(ctx, projectID)
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
	// The same classification the upload used: isTestFile, not isJMX -- a
	// k6 script is a test file too, and classifying it as data deleted from
	// the wrong table, leaving the record behind and 404ing the delete.
	isTest := isTestFile(filename)
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
