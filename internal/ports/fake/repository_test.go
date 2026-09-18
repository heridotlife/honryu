package fake_test

import (
	"context"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/execution"
	"github.com/heridotlife/honryu/internal/ports"
	"github.com/heridotlife/honryu/internal/ports/fake"
	"github.com/heridotlife/honryu/internal/ports/repositorytest"
)

func TestFakeProjectRepository_Contract(t *testing.T) {
	t.Parallel()
	repositorytest.RunProjectRepositoryContract(t, func(_ *testing.T) ports.ProjectRepository {
		return fake.NewProjectRepository()
	})
}

// A fan-out execution with an active run counts as running on EVERY cluster
// its target list names -- the cluster delete guard and the Clusters page
// both read this, and a mid-flight fan-out run holds each target's engines
// exactly as a single-cluster one does.
func TestExecutionsWithActiveRunOnCluster_MatchesFanOutTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fake.NewStore()

	plain, _ := execution.New("plain", 1)
	plain.Cluster = "solo-1"
	plainID, _ := s.CreateExecution(ctx, plain)

	fan, _ := execution.New("everywhere", 1)
	fan.FanOutTargets = []string{"eu-1", "us-1"}
	fanID, _ := s.CreateExecution(ctx, fan)

	// Nothing running yet: no cluster sees either execution.
	for _, cluster := range []string{"solo-1", "eu-1", "us-1"} {
		if ids, err := s.ExecutionsWithActiveRunOnCluster(ctx, cluster); err != nil || len(ids) != 0 {
			t.Fatalf("ExecutionsWithActiveRunOnCluster(%s) before any run = %v, %v; want none", cluster, ids, err)
		}
	}

	if _, err := s.StartRun(ctx, fanID, ""); err != nil {
		t.Fatalf("StartRun(fan-out): %v", err)
	}
	for _, cluster := range []string{"eu-1", "us-1"} {
		ids, err := s.ExecutionsWithActiveRunOnCluster(ctx, cluster)
		if err != nil || len(ids) != 1 || ids[0] != fanID {
			t.Fatalf("ExecutionsWithActiveRunOnCluster(%s) = %v, %v; want [%d]", cluster, ids, err, fanID)
		}
	}
	// A cluster that is not a target sees nothing -- a fan-out run does not
	// make every cluster hold it.
	if ids, err := s.ExecutionsWithActiveRunOnCluster(ctx, "solo-1"); err != nil || len(ids) != 0 {
		t.Fatalf("ExecutionsWithActiveRunOnCluster(solo-1) = %v, %v; want none", ids, err)
	}
	_ = plainID
}
