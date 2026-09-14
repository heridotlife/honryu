// Package projectapp implements the application use-cases for projects. It
// orchestrates the pure domain (internal/domain/project) over the repository
// ports, and holds no infrastructure dependencies of its own.
package projectapp

import (
	"context"
	"errors"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/domain/project"
	"github.com/heridotlife/honryu/internal/domain/scenario"
	"github.com/heridotlife/honryu/internal/ports"
)

// Business-rule errors. Callers compare with errors.Is.
var (
	ErrProjectHasScenarios  = errors.New("projectapp: cannot delete a project that has scenarios")
	ErrProjectHasExecutions = errors.New("projectapp: cannot delete a project that has executions")
)

// Repo is the repository surface the project service needs: project CRUD,
// the child listings used to enforce delete rules, and the template catalog
// the dashboard summary counts (fake and mysql both already carry it).
type Repo interface {
	ports.ProjectRepository
	ListScenariosByProject(ctx context.Context, projectID int64) ([]scenario.Scenario, error)
	ListExecutionsByProject(ctx context.Context, projectID int64) ([]execution.Execution, error)
	ListTemplates(ctx context.Context) ([]scenario.Scenario, error)
}

// Service provides project use-cases.
type Service struct {
	repo Repo
	// reports optionally backs the dashboard summary (Summary): the same
	// report surface the trend and digest services read. Nil keeps every
	// other use-case working and makes Summary answer the count-only shape
	// (no runs, no series) rather than fail -- a project service wired
	// without reports is a deployment without report data, and the counts
	// it can answer honestly it still answers.
	reports ports.ReportStore
}

// NewService wires a Service to a project repository.
func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

// List returns the projects owned by any of owners.
func (s *Service) List(ctx context.Context, owners []string) ([]project.Project, error) {
	return s.repo.ListProjectsByOwners(ctx, owners)
}

// ListAll returns every project (service-provider admin view under RBAC).
func (s *Service) ListAll(ctx context.Context) ([]project.Project, error) {
	return s.repo.ListAllProjects(ctx)
}

// ListByTenants returns the projects belonging to any of tenantIDs (tenant-scoped
// view under RBAC).
func (s *Service) ListByTenants(ctx context.Context, tenantIDs []int64) ([]project.Project, error) {
	return s.repo.ListProjectsByTenants(ctx, tenantIDs)
}

// Get returns a single project by ID (ports.ErrNotFound if absent).
func (s *Service) Get(ctx context.Context, id int64) (project.Project, error) {
	return s.repo.GetProject(ctx, id)
}

// Create validates the input, persists a new project, and returns it with its
// assigned ID.
func (s *Service) Create(ctx context.Context, name, owner, sid string) (project.Project, error) {
	return s.CreateInTenant(ctx, name, owner, sid, nil)
}

// CreateInTenant is Create with an optional owning tenant (RBAC multi-tenancy).
func (s *Service) CreateInTenant(ctx context.Context, name, owner, sid string, tenantID *int64) (project.Project, error) {
	p, err := project.New(name, owner, sid)
	if err != nil {
		return project.Project{}, err
	}
	p.TenantID = tenantID
	id, err := s.repo.CreateProject(ctx, p)
	if err != nil {
		return project.Project{}, err
	}
	p.ID = id
	return p, nil
}

// Delete removes a project by ID. It refuses to delete a project that still has
// scenarios or executions (ports.ErrNotFound if the project is absent).
func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.repo.GetProject(ctx, id); err != nil {
		return err
	}
	scenarios, err := s.repo.ListScenariosByProject(ctx, id)
	if err != nil {
		return err
	}
	if len(scenarios) > 0 {
		return ErrProjectHasScenarios
	}
	executions, err := s.repo.ListExecutionsByProject(ctx, id)
	if err != nil {
		return err
	}
	if len(executions) > 0 {
		return ErrProjectHasExecutions
	}
	return s.repo.DeleteProject(ctx, id)
}
