package apps

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
)

// limitResources sets the child's ceilings: the Go runtime's memory limit,
// which makes the collector work harder near it, and the kernel's limits
// on address space and CPU time, which end the process past them. The
// address space is counted from what the runtime holds once started, so
// the ceiling is renderMemory more than that.
func limitResources() error {
	debug.SetMemoryLimit(renderMemory / 2)
	as := addressSpace() + renderMemory
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: as, Max: as}); err != nil {
		return err
	}
	return syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: renderCPUSeconds, Max: renderCPUSeconds})
}

// addressSpace is the process's address space now, in bytes.
func addressSpace() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	pages, err := strconv.ParseUint(strings.Fields(string(b) + " 0")[0], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
