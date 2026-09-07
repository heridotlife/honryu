// Package executionapp implements the application use-cases for executions:
// CRUD, data files, and the execution configuration (which scenarios run, with how
// many engines). Storage keys follow "execution/{id}/{filename}".
package executionapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/loadprofile"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/domain/taurus"
	"github.com/heridotlife/honryu/internal/ports"
)

// Business-rule errors. Callers compare with errors.Is.
var (
	ErrInvalidFilename      = errors.New("executionapp: invalid filename")
	ErrExecutionMismatch    = errors.New("executionapp: config execution id does not match target")
	ErrScenarioNotInProject = errors.New("executionapp: scenario does not belong to the execution's project")
	ErrEngineLimit          = errors.New("executionapp: requested engines exceed the cluster limit")
)

// Repo is the repository surface the execution service needs, including
// GetScenario to validate execution config against real scenarios.
type Repo interface {
	CreateExecution(ctx context.Context, c execution.Execution) (int64, error)
	GetExecution(ctx context.Context, id int64) (execution.Execution, error)
	ListExecutionsByProject(ctx context.Context, projectID int64) ([]execution.Execution, error)
	// ListExecutionsByProjects backs the cross-project operator listing.
	ListExecutionsByProjects(ctx context.Context, projectIDs []int64) ([]execution.Execution, error)
	DeleteExecution(ctx context.Context, id int64) error
	AddExecutionFile(ctx context.Context, executionID int64, filename string) error
	ExecutionFilesFor(ctx context.Context, executionID int64) ([]string, error)
	DeleteExecutionFile(ctx context.Context, executionID int64, filename string) error
	LoadProfileFor(ctx context.Context, executionID int64) ([]loadprofile.Entry, error)
	CriteriaFor(ctx context.Context, executionID int64) ([]string, error)
	// StoreExecutionConfig replaces the load profile and criteria together,
	// atomically -- StoreConfig is one config upload, and a caller must
	// never see it partially applied.
	StoreExecutionConfig(ctx context.Context, executionID int64, csvSplit bool, entries []loadprofile.Entry, criteria []string) error
	GetScenario(ctx context.Context, id int64) (scenario.Scenario, error)
	// GetProject resolves the owning project's tenant so Create can stamp it
	// onto the execution (Phase 20 tenant propagation).
	GetProject(ctx context.Context, id int64) (project.Project, error)
}

// Service provides execution use-cases.
type Service struct {
	repo       Repo
	store      ports.ObjectStore
	maxEngines int
}

// NewService wires a Service. maxEngines caps the total engines a execution's
// execution config may request.
func NewService(repo Repo, store ports.ObjectStore, maxEngines int) *Service {
	return &Service{repo: repo, store: store, maxEngines: maxEngines}
}

// FileRef describes a stored file and how to fetch it.
type FileRef struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// Create validates input and persists a new execution. An empty engine or
// cluster means the caller expressed no preference and takes the deployment's
// configured default at deploy time.
func (s *Service) Create(ctx context.Context, name string, projectID int64, engine taurus.Executor, cluster string) (execution.Execution, error) {
	c, err := execution.New(name, projectID)
	if err != nil {
		return execution.Execution{}, err
	}
	c.Engine = engine
	c.Cluster = strings.TrimSpace(cluster)
	// The execution's tenant always equals its project's tenant (Phase 20).
	// A missing project is tolerated here rather than failing Create: the
	// HTTP layer already 404s on an unknown project before ever reaching
	// this call (authorizeProject fetches it first), so ErrNotFound here
	// means only a test or future caller that skipped that check -- and it
	// should get the same untenanted execution Create always produced, not
	// a new failure mode.
	if p, err := s.repo.GetProject(ctx, projectID); err == nil {
		c.TenantID = p.TenantID
	} else if !errors.Is(err, ports.ErrNotFound) {
		return execution.Execution{}, err
	}
	if err := c.Validate(); err != nil {
		return execution.Execution{}, err
	}
	id, err := s.repo.CreateExecution(ctx, c)
	if err != nil {
		return execution.Execution{}, err
	}
	c.ID = id
	return c, nil
}

// Get returns an execution by ID (ports.ErrNotFound if absent).
func (s *Service) Get(ctx context.Context, id int64) (execution.Execution, error) {
	return s.repo.GetExecution(ctx, id)
}

// ListByProject returns the executions of a project.
func (s *Service) ListByProject(ctx context.Context, projectID int64) ([]execution.Execution, error) {
	return s.repo.ListExecutionsByProject(ctx, projectID)
}

// ListForProjects returns every execution belonging to any of projectIDs,
// newest first -- the operator's execution list. An empty project list yields
// an empty result, never every execution.
func (s *Service) ListForProjects(ctx context.Context, projectIDs []int64) ([]execution.Execution, error) {
	if len(projectIDs) == 0 {
		return []execution.Execution{}, nil
	}
	return s.repo.ListExecutionsByProjects(ctx, projectIDs)
}

// Delete removes an execution and its data files.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.repo.GetExecution(ctx, id); err != nil {
		return err
	}
	files, err := s.repo.ExecutionFilesFor(ctx, id)
	if err != nil {
		return err
	}
	for _, name := range files {
		if delErr := s.store.Delete(ctx, executionKey(id, name)); delErr != nil {
			return delErr
		}
	}
	return s.repo.DeleteExecution(ctx, id)
}

// Files lists a execution's data files with retrieval URLs.
func (s *Service) Files(ctx context.Context, executionID int64) ([]FileRef, error) {
	names, err := s.repo.ExecutionFilesFor(ctx, executionID)
	if err != nil {
		return nil, err
	}
	out := make([]FileRef, 0, len(names))
	for _, name := range names {
		out = append(out, FileRef{Filename: name, URL: s.store.URL(executionKey(executionID, name))})
	}
	return out, nil
}

// UploadFile records and stores an execution data file. Returns
// ports.ErrFileExists if it is already present.
func (s *Service) UploadFile(ctx context.Context, executionID int64, filename string, content io.Reader) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	if _, err := s.repo.GetExecution(ctx, executionID); err != nil {
		return err
	}
	if err := s.repo.AddExecutionFile(ctx, executionID, filename); err != nil {
		return err
	}
	if err := s.store.Upload(ctx, executionKey(executionID, filename), content); err != nil {
		_ = s.repo.DeleteExecutionFile(ctx, executionID, filename)
		return err
	}
	return nil
}

// DownloadFile returns the bytes of an execution data file.
func (s *Service) DownloadFile(ctx context.Context, executionID int64, filename string) ([]byte, error) {
	if err := validateFilename(filename); err != nil {
		return nil, err
	}
	return s.store.Download(ctx, executionKey(executionID, filename))
}

// DeleteFile removes an execution data file record and its stored object.
func (s *Service) DeleteFile(ctx context.Context, executionID int64, filename string) error {
	if err := validateFilename(filename); err != nil {
		return err
	}
	if err := s.repo.DeleteExecutionFile(ctx, executionID, filename); err != nil {
		return err
	}
	return s.store.Delete(ctx, executionKey(executionID, filename))
}

// StoreConfig validates and persists the execution configuration for a
// execution: every scenario must exist and belong to the execution's project, and
// the total engines must not exceed the configured limit.
func (s *Service) StoreConfig(ctx context.Context, executionID int64, ec loadprofile.Profile) error {
	coll, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return err
	}
	if ec.ExecutionID != executionID {
		return ErrExecutionMismatch
	}
	if err := ec.Validate(); err != nil {
		return err
	}
	for _, ep := range ec.Tests {
		p, planErr := s.repo.GetScenario(ctx, ep.ScenarioID)
		if planErr != nil {
			return planErr
		}
		if p.ProjectID != coll.ProjectID {
			return ErrScenarioNotInProject
		}
	}
	if total := ec.TotalEngines(); total > s.maxEngines {
		return fmt.Errorf("%w: requested %d, limit %d", ErrEngineLimit, total, s.maxEngines)
	}
	// One transaction: a config upload replaces the load profile and
	// criteria together, so a failure partway through can never leave the
	// execution with a new load profile but stale (or missing) criteria --
	// which would otherwise leave a campaign verdict unable to name a
	// failing criterion for this execution at all, silently.
	return s.repo.StoreExecutionConfig(ctx, executionID, ec.CSVSplit, ec.Tests, ec.Criteria)
}

// GetConfig returns the execution's current execution configuration wrapped
// for serialization.
func (s *Service) GetConfig(ctx context.Context, executionID int64) (loadprofile.Wrapper, error) {
	coll, err := s.repo.GetExecution(ctx, executionID)
	if err != nil {
		return loadprofile.Wrapper{}, err
	}
	scenarios, err := s.repo.LoadProfileFor(ctx, executionID)
	if err != nil {
		return loadprofile.Wrapper{}, err
	}
	criteria, err := s.repo.CriteriaFor(ctx, executionID)
	if err != nil {
		return loadprofile.Wrapper{}, err
	}
	return loadprofile.Wrapper{Content: loadprofile.Profile{
		Name:        coll.Name,
		ProjectID:   coll.ProjectID,
		ExecutionID: executionID,
		Tests:       scenarios,
		CSVSplit:    coll.CSVSplit,
		Criteria:    criteria,
	}}, nil
}

// CriteriaFor returns the execution's currently configured Taurus pass/fail
// criteria, in the order they were stored -- the run report's verdict layer
// reads them without paying for the rest of GetConfig.
func (s *Service) CriteriaFor(ctx context.Context, executionID int64) ([]string, error) {
	return s.repo.CriteriaFor(ctx, executionID)
}

func executionKey(executionID int64, filename string) string {
	return fmt.Sprintf("execution/%d/%s", executionID, filename)
}

func validateFilename(filename string) error {
	if filename == "" || strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." {
		return ErrInvalidFilename
	}
	return nil
}
