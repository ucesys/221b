package main

import (
	"testing"

	"github.com/ucesys/221b/server/internal/jobs"
)

// The jobs' keys (Phase 12) are bounded in their ConfigMap of before. The
// runs no longer trim themselves to it (DB-80).
func TestTheJobsKeysHaveRoom(t *testing.T) {
	l := limits(objRuns)
	if l[jobs.JobsKey].Bytes == 0 || l[jobs.RunsKey].Bytes == 0 {
		t.Fatalf("the jobs' keys have no limit: %+v", l)
	}
	if jobs.DefaultRunsBytes > l[jobs.RunsKey].Bytes {
		t.Fatalf("the runs' default is %d bytes of their %d", jobs.DefaultRunsBytes, l[jobs.RunsKey].Bytes)
	}
}
