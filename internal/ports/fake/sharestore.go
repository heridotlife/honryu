package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/report"
	"github.com/heridotlife/honryu/internal/ports"
)

// ShareStore is an in-memory ports.ShareStore for fast use-case tests.
type ShareStore struct {
	mu sync.Mutex
	// shares maps token -> share; the list-by-run view filters and sorts
	// this, which is also what makes duplicate tokens overwrite (a mint
	// collision) visible rather than silently doubled.
	shares map[string]report.ShareToken
	seq    int64

	// CreateErr, when set, is returned by CreateShare.
	CreateErr error
	// GetErr, when set, is returned by GetShareByToken instead of its usual
	// result -- a transient store failure, distinct from ErrNotFound.
	GetErr error
	// ListErr, when set, is returned by ListSharesByRun.
	ListErr error
	// DeleteErr, when set, is returned by DeleteShare.
	DeleteErr error
}

// NewShareStore builds an empty store.
func NewShareStore() *ShareStore {
	return &ShareStore{shares: map[string]report.ShareToken{}}
}

var _ ports.ShareStore = (*ShareStore)(nil)

// CreateShare records a newly minted token, assigning the row id.
func (s *ShareStore) CreateShare(_ context.Context, runID int64, token, createdBy string, expires *time.Time) error {
	if s.CreateErr != nil {
		return s.CreateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.shares[token] = report.ShareToken{
		ID: s.seq, RunID: runID, Token: token, CreatedBy: createdBy,
		CreatedTime: time.Now().UTC(), Expires: expires,
	}
	return nil
}

// GetShareByToken resolves a token, or ports.ErrNotFound.
func (s *ShareStore) GetShareByToken(_ context.Context, token string) (report.ShareToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetErr != nil {
		return report.ShareToken{}, s.GetErr
	}
	got, ok := s.shares[token]
	if !ok {
		return report.ShareToken{}, ports.ErrNotFound
	}
	return got, nil
}

// ListSharesByRun returns a run's links, oldest first.
func (s *ShareStore) ListSharesByRun(_ context.Context, runID int64) ([]report.ShareToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ListErr != nil {
		return nil, s.ListErr
	}
	var out []report.ShareToken
	for _, sh := range s.shares {
		if sh.RunID == runID {
			out = append(out, sh)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// DeleteShare removes one token, or ports.ErrNotFound.
func (s *ShareStore) DeleteShare(_ context.Context, runID int64, token string) error {
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[token]
	if !ok || sh.RunID != runID {
		return ports.ErrNotFound
	}
	delete(s.shares, token)
	return nil
}
