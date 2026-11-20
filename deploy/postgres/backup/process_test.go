package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The program as the container runs it, with stand-ins for Patroni and
// WAL-G: it takes a base backup, and it stops within a second when told
// to, while it waits and while WAL-G runs, ending with 0. BACKUP_BIN runs
// a program built elsewhere (the one in the image) in place of this
// package's.
func program(t *testing.T) string {
	if bin := os.Getenv("BACKUP_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "221b-backup")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return bin
}

// start runs the program with a WAL-G whose backup-push takes push, and
// says where WAL-G's calls are written.
func start(t *testing.T, bin string, push time.Duration, env ...string) (*exec.Cmd, string, *syncBuffer) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	walg := fmt.Sprintf("#!/bin/sh\necho \"$1\" >> %s\ncase \"$1\" in\nbackup-list) echo '[]' ;;\nbackup-push) exec sleep %g ;;\nesac\n", calls, push.Seconds())
	if err := os.WriteFile(filepath.Join(dir, "wal-g"), []byte(walg), 0o755); err != nil {
		t.Fatal(err)
	}
	patroni := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"members":[{"name":"db-0","role":"leader","state":"running","timeline":1}]}`)
	}))
	t.Cleanup(patroni.Close)
	cmd := exec.Command(bin)
	cmd.Env = append([]string{"PATH=" + dir + ":" + os.Getenv("PATH"), "PGDATA=/nowhere", "PATRONI_NAME=db-0", "DB_BACKUP_PATRONI=" + patroni.URL, "DB_BACKUP_POLL=100ms"}, env...)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd, calls, out
}

// stop sends SIGTERM and says how long the program took to end.
func stop(t *testing.T, cmd *exec.Cmd, out *syncBuffer) time.Duration {
	t0 := time.Now()
	cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("it ended with %v:\n%s", err, out)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("it did not stop:\n%s", out)
	}
	return time.Since(t0)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func pushed(calls string) bool {
	b, _ := os.ReadFile(calls)
	return strings.Contains(string(b), "backup-push")
}

func TestTheContainerStopsWhenTold(t *testing.T) {
	bin := program(t)

	// While it waits.
	cmd, _, out := start(t, bin, 0, "DB_BACKUP_SCHEDULE=59 23 * * *", "DB_BACKUP_POLL=30s")
	waitFor(t, "the first line", func() bool { return strings.Contains(out.String(), "a base backup on the schedule") })
	if took := stop(t, cmd, out); took > time.Second {
		t.Fatalf("it took %v to stop while it waited", took)
	}

	// During a base backup.
	cmd, calls, out := start(t, bin, 30*time.Second, "DB_BACKUP_SCHEDULE=0 0 * * *")
	waitFor(t, "a base backup", func() bool { return pushed(calls) })
	if took := stop(t, cmd, out); took > time.Second {
		t.Fatalf("it took %v to stop during a base backup", took)
	}
	if !strings.Contains(out.String(), "stopping") {
		t.Fatalf("no word of stopping:\n%s", out)
	}
}

func TestTheContainerTakesABaseBackupAndKeepsTheNumberGiven(t *testing.T) {
	bin := program(t)
	cmd, calls, out := start(t, bin, 0, "DB_BACKUP_AT=00:00", "DB_BACKUP_RETAIN=3")
	waitFor(t, "the old backups removed", func() bool {
		b, _ := os.ReadFile(calls)
		return strings.Contains(string(b), "delete")
	})
	stop(t, cmd, out)
	b, _ := os.ReadFile(calls)
	if got := strings.Fields(string(b)); strings.Join(got, " ") != "backup-list backup-push delete" {
		t.Fatalf("WAL-G was asked %q\n%s", got, out)
	}
}

func TestAScheduleThatCannotBeReadStopsTheContainer(t *testing.T) {
	bin := program(t)
	cmd := exec.Command(bin)
	cmd.Env = []string{"PGDATA=/nowhere", "DB_BACKUP_SCHEDULE=every night"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "five fields") {
		t.Fatalf("%v: %s", err, out)
	}
}

// syncBuffer is the program's output, written by exec's goroutine and
// read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
