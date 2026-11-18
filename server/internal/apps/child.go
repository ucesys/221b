package apps

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
)

// A chart renders in a process of its own: the console's binary started
// again as "221b render-chart" (RenderChartCommand). The child takes the
// chart, the values and the release on stdin, sets ceilings on its own
// memory and CPU time before it renders, and writes what it rendered, or
// a plain error, on stdout. The console waits for it as long as
// renderTimeout and kills it then, so its slot and its CPU come back; a
// child that goes past its memory or CPU dies with the render, and the
// console goes on.

// RenderChartCommand is the hidden subcommand that renders one chart.
const RenderChartCommand = "render-chart"

// renderChildEnv marks the process started to render: whatever binary it
// is (the console, or a test binary), it renders and exits before main.
const renderChildEnv = "RENDER_221B_CHART"

// Exit codes of the child besides 0.
const (
	childFailed    = 1
	childOverLimit = 3
)

// renderTimeout is how long a caller waits for one render.
var renderTimeout = 60 * time.Second

// renderSlots is how many renders run at once.
var renderSlots = make(chan struct{}, 2)

func init() {
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register(common.Values{})
	if os.Getenv(renderChildEnv) == "1" {
		os.Exit(RenderChartMain(os.Stdin, os.Stdout))
	}
}

// ReleaseOptions say what release a chart renders for.
type ReleaseOptions struct {
	Name, Namespace string
	// KubeVersion and APIVersions are the cluster's, when known; Helm's
	// defaults stand in otherwise.
	KubeVersion string
	APIVersions []string
	// Upgrade renders an upgrade to Revision of a release deployed with
	// the values Previous; an install of revision 1 otherwise.
	Upgrade  bool
	Revision int
	Previous map[string]any
	// HideSecret leaves Secret objects out of the manifest.
	HideSecret bool
}

// renderRequest is what the child reads.
type renderRequest struct {
	Chart  wireChart
	Values map[string]any
	Opts   ReleaseOptions
}

// wireChart is a loaded chart with its subcharts, as the child gets it.
type wireChart struct {
	Metadata      *chart.Metadata
	Lock          *chart.Lock
	Templates     []*common.File
	Values        map[string]any
	Schema        []byte
	SchemaModTime time.Time
	Files         []*common.File
	ModTime       time.Time
	Dependencies  []wireChart
}

func toWire(ch *chart.Chart) wireChart {
	w := wireChart{Metadata: ch.Metadata, Lock: ch.Lock, Templates: ch.Templates, Values: ch.Values, Schema: ch.Schema, SchemaModTime: ch.SchemaModTime, Files: ch.Files, ModTime: ch.ModTime}
	for _, d := range ch.Dependencies() {
		w.Dependencies = append(w.Dependencies, toWire(d))
	}
	return w
}

func (w wireChart) chart() *chart.Chart {
	ch := &chart.Chart{Metadata: w.Metadata, Lock: w.Lock, Templates: w.Templates, Values: w.Values, Schema: w.Schema, SchemaModTime: w.SchemaModTime, Files: w.Files, ModTime: w.ModTime}
	var deps []*chart.Chart
	for _, d := range w.Dependencies {
		deps = append(deps, d.chart())
	}
	ch.SetDependencies(deps...)
	return ch
}

// errOverResources is the refusal for a child that went past its memory
// or CPU time and was ended for it.
var errOverResources = fmt.Errorf("%w: its templates used more memory or time than allowed", ErrRenderLimit)

// renderOutside renders in a child process, waiting for a slot first.
func renderOutside(ctx context.Context, ch *chart.Chart, vals map[string]any, o ReleaseOptions) (Rendered, error) {
	var req bytes.Buffer
	if err := gob.NewEncoder(&req).Encode(renderRequest{Chart: toWire(ch), Values: vals, Opts: o}); err != nil {
		return Rendered{}, fmt.Errorf("the chart could not be handed to the renderer: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return Rendered{}, fmt.Errorf("the renderer could not be started: %w", err)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	select {
	case renderSlots <- struct{}{}:
	case <-ctx.Done():
		if parent.Err() != nil {
			return Rendered{}, parent.Err()
		}
		return Rendered{}, fmt.Errorf("%w: other charts are still rendering, try again later", ErrRenderLimit)
	}
	defer func() { <-renderSlots }()

	cmd := exec.CommandContext(ctx, exe, RenderChartCommand)
	// The child needs nothing of the console's environment.
	cmd.Env = []string{renderChildEnv + "=1", "GOMAXPROCS=2"}
	cmd.Stdin = &req
	var stderr limitedBuffer
	stderr.max = 16 << 10
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Rendered{}, fmt.Errorf("the renderer could not be started: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Rendered{}, fmt.Errorf("the renderer could not be started: %w", err)
	}
	// What comes back is the manifest and the notes, with a little room
	// for their encoding.
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxRenderOutput+64<<10+1))
	tooMuch := len(out) > maxRenderOutput+64<<10
	if tooMuch {
		cancel()
	}
	waitErr := cmd.Wait()
	switch {
	case parent.Err() != nil:
		return Rendered{}, parent.Err()
	case tooMuch:
		return Rendered{}, fmt.Errorf("%w: it renders to more than %d MB", ErrRenderLimit, maxRenderOutput>>20)
	case ctx.Err() != nil:
		return Rendered{}, fmt.Errorf("%w: it did not finish rendering in time", ErrRenderLimit)
	case waitErr == nil && readErr == nil:
		var r Rendered
		if err := gob.NewDecoder(bytes.NewReader(out)).Decode(&r); err != nil {
			return Rendered{}, fmt.Errorf("the renderer's answer could not be read: %w", err)
		}
		return r, nil
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		msg := strings.TrimSpace(string(out))
		switch exit.ExitCode() {
		case childFailed:
			return Rendered{}, errors.New(msg)
		case childOverLimit:
			return Rendered{}, fmt.Errorf("%w: %s", ErrRenderLimit, msg)
		}
		// Killed for its CPU time, or the runtime gave up for want of
		// memory.
		return Rendered{}, errOverResources
	}
	return Rendered{}, fmt.Errorf("the renderer failed: %v", errors.Join(waitErr, readErr))
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// RenderChartMain is the child: it reads one request on in, renders it
// within its ceilings and writes the result on out. It returns the exit
// code.
func RenderChartMain(in io.Reader, out io.Writer) int {
	if err := limitResources(); err != nil {
		fmt.Fprintf(out, "the renderer could not limit itself: %v", err)
		return childFailed
	}
	var req renderRequest
	if err := gob.NewDecoder(in).Decode(&req); err != nil {
		fmt.Fprintf(out, "the renderer could not read the chart: %v", err)
		return childFailed
	}
	r, err := renderHere(context.Background(), req.Chart.chart(), req.Values, req.Opts)
	if errors.Is(err, ErrRenderLimit) {
		fmt.Fprint(out, strings.TrimPrefix(err.Error(), ErrRenderLimit.Error()+": "))
		return childOverLimit
	}
	if err != nil {
		fmt.Fprint(out, err.Error())
		return childFailed
	}
	if err := gob.NewEncoder(out).Encode(r); err != nil {
		return childFailed
	}
	return 0
}
