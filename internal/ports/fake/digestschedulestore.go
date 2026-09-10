package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/domain/digest"
	"github.com/heridotlife/honryu/internal/ports"
)

// DigestScheduleStore is an in-memory ports.DigestScheduleStore for fast
// use-case tests. The store-wide mutex is the claim's atomicity: two
// concurrent ClaimDueDigestSchedule calls serialize, and the second sees
// the first's last_fired stamp -- the same guarantee the MySQL adapter
// derives from the conditional UPDATE's affected rows.
type DigestScheduleStore struct {
	mu        sync.Mutex
	schedules map[int64]digest.Schedule

	// UpsertErr, when set, is returned by UpsertDigestSchedule.
	UpsertErr error
	// GetErr, when set, is returned by GetDigestSchedule.
	GetErr error
	// DeleteErr, when set, is returned by DeleteDigestSchedule.
	DeleteErr error
	// ClaimErr, when set, is returned by ClaimDueDigestSchedule.
	ClaimErr error
}

// NewDigestScheduleStore builds an empty store.
func NewDigestScheduleStore() *DigestScheduleStore {
	return &DigestScheduleStore{schedules: map[int64]digest.Schedule{}}
}

var _ ports.DigestScheduleStore = (*DigestScheduleStore)(nil)

// UpsertDigestSchedule stores the configuration, keeping any existing
// last_fired bookmark.
func (s *DigestScheduleStore) UpsertDigestSchedule(_ context.Context, projectID int64, period digest.Period, enabled bool) error {
	if s.UpsertErr != nil {
		return s.UpsertErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.schedules[projectID]
	row := digest.Schedule{ProjectID: projectID, Period: period, Enabled: enabled}
	if ok {
		row.LastFired = prev.LastFired
	}
	s.schedules[projectID] = row
	return nil
}

// GetDigestSchedule returns the project's schedule, or ErrNotFound.
func (s *DigestScheduleStore) GetDigestSchedule(_ context.Context, projectID int64) (digest.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetErr != nil {
		return digest.Schedule{}, s.GetErr
	}
	got, ok := s.schedules[projectID]
	if !ok {
		return digest.Schedule{}, ports.ErrNotFound
	}
	return got, nil
}

// DeleteDigestSchedule removes the project's schedule, or ErrNotFound.
func (s *DigestScheduleStore) DeleteDigestSchedule(_ context.Context, projectID int64) error {
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.schedules[projectID]; !ok {
		return ports.ErrNotFound
	}
	delete(s.schedules, projectID)
	return nil
}

// ClaimDueDigestSchedule claims one due, enabled schedule: the never-fired
// first, then oldest last_fired first, stamping last_fired=now only when
// the row is still due under its own period.
func (s *DigestScheduleStore) ClaimDueDigestSchedule(_ context.Context, now time.Time) (digest.Schedule, bool, error) {
	if s.ClaimErr != nil {
		return digest.Schedule{}, false, s.ClaimErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range dueOrder(s.schedules, now) {
		row := s.schedules[candidate]
		// Re-check dueness under the lock (the candidate list was built
		// from the same locked snapshot, but the re-check documents the
		// contract the MySQL adapter enforces through affected rows).
		if due(row, now) {
			stamped := now
			row.LastFired = &stamped
			s.schedules[candidate] = row
			return row, true, nil
		}
	}
	return digest.Schedule{}, false, nil
}

// dueOrder returns the ids of schedules that are enabled and due, never
// fired first, then oldest last_fired first.
func dueOrder(rows map[int64]digest.Schedule, now time.Time) []int64 {
	var ids []int64
	for id, row := range rows {
		if row.Enabled && due(row, now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := rows[ids[i]], rows[ids[j]]
		switch {
		case a.LastFired == nil && b.LastFired != nil:
			return true
		case a.LastFired != nil && b.LastFired == nil:
			return false
		case a.LastFired != nil && b.LastFired != nil:
			return a.LastFired.Before(*b.LastFired)
		default:
			return ids[i] < ids[j]
		}
	})
	return ids
}

// due is the single dueness rule: never fired, or the period elapsed.
func due(row digest.Schedule, now time.Time) bool {
	return row.LastFired == nil || !now.Before(row.LastFired.Add(row.Period.Duration()))
}
