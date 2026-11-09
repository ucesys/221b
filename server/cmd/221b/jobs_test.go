package main

import (
	"testing"

	"github.com/ucesys/221b/server/internal/jobs"
)

// The jobs' keys (Phase 12) are bounded in their own ConfigMap, and the
// runs trim themselves below their key's limit.
func TestTheJobsKeysHaveRoom(t *testing.T) {
	l := limits(objRuns)
	if l[jobs.JobsKey].Bytes == 0 || l[jobs.RunsKey].Bytes == 0 {
		t.Fatalf("the jobs' keys have no limit: %+v", l)
	}
	if n := runsLimit() - 16<<10; n <= 0 || n >= l[jobs.RunsKey].Bytes || jobs.DefaultRunsBytes > l[jobs.RunsKey].Bytes {
		t.Fatalf("the runs fit to %d bytes of their %d", n, l[jobs.RunsKey].Bytes)
	}
}
