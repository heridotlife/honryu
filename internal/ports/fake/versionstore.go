package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/heridotlife/honryu/internal/ports"
)

// VersionStore is the in-memory ScenarioVersionStore: versions keyed by
// scenario, appended under one mutex. Its own mutex (not Store's) — none of
// Store's internals mediate these rows, exactly like the embedded
// ThresholdStore.
type VersionStore struct {
	mu       sync.Mutex
	versions map[int64][]ports.ScenarioVersion // scenarioID -> ascending by version
	seq      int64                             // storage row ids, global
	nextNum  map[int64]int                     // scenarioID -> next version number

	// AppendErr, when set, is returned by AppendScenarioVersion.
	AppendErr error
	// ListErr, when set, is returned by ListScenarioVersions.
	ListErr error
	// GetErr, when set, is returned by ScenarioVersion.
	GetErr error
}

// NewVersionStore builds an empty store.
func NewVersionStore() *VersionStore {
	return &VersionStore{
		versions: make(map[int64][]ports.ScenarioVersion),
		nextNum:  make(map[int64]int),
	}
}

var _ ports.ScenarioVersionStore = (*VersionStore)(nil)

// AppendScenarioVersion records snapshot as scenarioID's next version
// (max+1) and returns the number assigned. The version number and the row
// id are the store's to assign; an empty createdBy is stored as nil (the
// no-actor case is NULL on the wire, never an empty name).
func (s *VersionStore) AppendScenarioVersion(_ context.Context, scenarioID int64, snapshot ports.ScenarioSnapshot, createdBy string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.AppendErr != nil {
		return 0, s.AppendErr
	}
	var actor *string
	if createdBy != "" {
		name := createdBy
		actor = &name
	}
	s.seq++
	version := s.nextNum[scenarioID] + 1
	s.nextNum[scenarioID] = version
	v := ports.ScenarioVersion{
		ScenarioVersionMeta: ports.ScenarioVersionMeta{
			ID:          s.seq,
			ScenarioID:  scenarioID,
			Version:     version,
			CreatedTime: time.Now().UTC(),
			CreatedBy:   actor,
		},
		Snapshot: snapshot,
	}
	s.versions[scenarioID] = append(s.versions[scenarioID], v)
	return version, nil
}

// ListScenarioVersions returns the scenario's versions, newest first.
// Always non-nil.
func (s *VersionStore) ListScenarioVersions(_ context.Context, scenarioID int64) ([]ports.ScenarioVersionMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ListErr != nil {
		return nil, s.ListErr
	}
	stored := s.versions[scenarioID]
	out := make([]ports.ScenarioVersionMeta, 0, len(stored))
	for i := len(stored) - 1; i >= 0; i-- {
		out = append(out, stored[i].ScenarioVersionMeta)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// ScenarioVersion returns one version, snapshot included:
// ports.ErrScenarioVersionNotFound when the scenario has no such version.
func (s *VersionStore) ScenarioVersion(_ context.Context, scenarioID int64, version int) (ports.ScenarioVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.GetErr != nil {
		return ports.ScenarioVersion{}, s.GetErr
	}
	for _, v := range s.versions[scenarioID] {
		if v.Version == version {
			return v, nil
		}
	}
	return ports.ScenarioVersion{}, ports.ErrScenarioVersionNotFound
}
