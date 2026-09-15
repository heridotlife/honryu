package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/slo"
	"github.com/heridotlife/honryu/internal/ports"
)

// SLOStore is an in-memory ports.SLOStore for fast use-case tests.
type SLOStore struct {
	mu   sync.Mutex
	slos map[int64]slo.SLO
	seq  int64

	// CreateErr, when set, is returned by CreateSLO.
	CreateErr error
	// ListErr, when set, is returned by ListSLOsByProject.
	ListErr error
	// GetErr, when set, is returned by GetSLO.
	GetErr error
	// DeleteErr, when set, is returned by DeleteSLO.
	DeleteErr error
}

// NewSLOStore builds an empty store.
func NewSLOStore() *SLOStore {
	return &SLOStore{slos: map[int64]slo.SLO{}}
}

var _ ports.SLOStore = (*SLOStore)(nil)

// CreateSLO records an objective, assigning the row id and stamp.
func (s *SLOStore) CreateSLO(_ context.Context, obj slo.SLO) (int64, error) {
	if s.CreateErr != nil {
		return 0, s.CreateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	obj.ID = s.seq
	obj.CreatedTime = time.Now().UTC()
	s.slos[obj.ID] = obj
	return obj.ID, nil
}

// ListSLOsByProject returns the project's SLOs, oldest first.
func (s *SLOStore) ListSLOsByProject(_ context.Context, projectID int64) ([]slo.SLO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ListErr != nil {
		return nil, s.ListErr
	}
	var out []slo.SLO
	for _, obj := range s.slos {
		if obj.ProjectID == projectID {
			out = append(out, obj)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetSLO returns one SLO, or ports.ErrNotFound when the id is absent or
// lives under a different project.
func (s *SLOStore) GetSLO(_ context.Context, projectID, id int64) (slo.SLO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetErr != nil {
		return slo.SLO{}, s.GetErr
	}
	obj, ok := s.slos[id]
	if !ok || obj.ProjectID != projectID {
		return slo.SLO{}, ports.ErrNotFound
	}
	return obj, nil
}

// DeleteSLO removes one SLO, or ports.ErrNotFound under the same
// project-scoping rule as GetSLO.
func (s *SLOStore) DeleteSLO(_ context.Context, projectID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	obj, ok := s.slos[id]
	if !ok || obj.ProjectID != projectID {
		return ports.ErrNotFound
	}
	delete(s.slos, id)
	return nil
}
