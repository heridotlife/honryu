package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/ports"
)

// DigestStore is an in-memory ports.ReportDigestStore for fast use-case
// tests.
type DigestStore struct {
	mu      sync.Mutex
	digests map[int64]digest.Digest
	seq     int64

	// SaveErr, when set, is returned by SaveDigest.
	SaveErr error
	// ListErr, when set, is returned by ListDigestsByProject.
	ListErr error
	// LastWindowEndErr, when set, is returned by LastDigestWindowEnd.
	LastWindowEndErr error
}

// NewDigestStore builds an empty store.
func NewDigestStore() *DigestStore {
	return &DigestStore{digests: map[int64]digest.Digest{}}
}

var _ ports.ReportDigestStore = (*DigestStore)(nil)

// SaveDigest records a digest, assigning the row id and stamp.
func (s *DigestStore) SaveDigest(_ context.Context, d digest.Digest) (int64, error) {
	if s.SaveErr != nil {
		return 0, s.SaveErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	d.ID = s.seq
	d.CreatedTime = time.Now().UTC()
	s.digests[d.ID] = d
	return d.ID, nil
}

// ListDigestsByProject returns the project's digests, newest first, with
// limit<=0 meaning no limit.
func (s *DigestStore) ListDigestsByProject(_ context.Context, projectID int64, limit int) ([]digest.Digest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ListErr != nil {
		return nil, s.ListErr
	}
	var out []digest.Digest
	for _, d := range s.digests {
		if d.ProjectID == projectID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// LastDigestWindowEnd returns the newest same-period digest's window_end,
// or found=false when the pairing has never fired.
func (s *DigestStore) LastDigestWindowEnd(_ context.Context, projectID int64, period digest.Period) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastWindowEndErr != nil {
		return time.Time{}, false, s.LastWindowEndErr
	}
	var (
		best  digest.Digest
		found bool
	)
	for _, d := range s.digests {
		if d.ProjectID != projectID || d.Period != period {
			continue
		}
		if !found || d.ID > best.ID {
			best = d
			found = true
		}
	}
	if !found {
		return time.Time{}, false, nil
	}
	return best.WindowEnd, true, nil
}
