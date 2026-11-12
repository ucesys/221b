// Package prompttest keeps a golden copy of every prompt and task the
// agents are given (P-3), in one directory, server/internal/ai/testdata/
// prompts, so that a review reads them in one place and a change to one
// shows in a diff. Each package that writes prompts renders its own with
// sample data in a test and calls Check; go test -update rewrites the
// copies (run it on the packages whose prompts changed, then read the
// diff).
//
// It is for tests only: it defines the -update flag, so a test binary
// that imports it must not define its own.
package prompttest

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden prompts in server/internal/ai/testdata/prompts")

// Dir is the golden directory, found from this file's own place.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata", "prompts")
}

// Check compares a prompt with its golden copy, name.txt, or writes the
// copy with -update. name is a file name without its extension, such as
// "job.audit".
func Check(t testing.TB, name, text string) {
	t.Helper()
	path := filepath.Join(Dir(), name+".txt")
	text = strings.TrimRight(text, "\n") + "\n"
	if *update {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s has no golden copy (%v): run go test with -update and read what it wrote", name, err)
	}
	if string(want) != text {
		t.Errorf("%s differs from its golden copy %s: if the change is meant, run go test with -update and read the diff\n%s", name, path, firstDiff(string(want), text))
	}
}

// firstDiff shows the first line that differs.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + strconv.Itoa(i+1) + ":\n  golden: " + a + "\n  now:    " + b
		}
	}
	return ""
}
