//go:build !linux

package apps

import "runtime/debug"

// limitResources sets the Go runtime's memory limit; the kernel's ceilings
// are set on Linux, where the console runs.
func limitResources() error {
	debug.SetMemoryLimit(renderMemory / 2)
	return nil
}
