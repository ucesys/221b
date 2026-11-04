package apps

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	chart "helm.sh/helm/v4/pkg/chart/v2"
)

// A chart's templates are untrusted code that runs inside the console. Go's
// templates cannot be stopped from outside once they run, so what a render
// may do is bounded around them: a render runs on its own goroutine under
// a timeout and takes one of a few slots until it really ends, the
// functions that generate loops and text spend a budget, and what comes out
// is measured. Helm itself bounds include's recursion.

const (
	// renderTimeout is how long a caller waits for one render.
	renderTimeout = 60 * time.Second
	// maxRenderOutput bounds what one render may produce.
	maxRenderOutput = 32 << 20
	// renderBudget bounds what until, untilStep, seq and repeat may
	// generate in one render. A loop counts each round as the chart's
	// largest template, the most text one round can write out without
	// another loop.
	renderBudget = 256 << 20
)

// ErrRenderLimit says a chart went past what a render may do.
var ErrRenderLimit = errors.New("the chart is too much for the console to render")

// renderSlots is how many renders run at once; a render that outlives its
// timeout keeps its slot until it ends.
var renderSlots = make(chan struct{}, 4)

// Bounded runs a render on its own goroutine and waits for it for
// renderTimeout at most. A render past that is left to end by itself, as
// nothing can stop a template: so only what may be abandoned runs here, a
// dry run and not a deploy.
func Bounded[T any](ctx context.Context, render func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	select {
	case renderSlots <- struct{}{}:
	case <-ctx.Done():
		return zero, fmt.Errorf("%w: other charts are still rendering, try again later", ErrRenderLimit)
	}
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-renderSlots }()
		defer func() {
			if r := recover(); r != nil {
				done <- result{zero, fmt.Errorf("rendering the chart: %v", r)}
			}
		}()
		v, err := render(ctx)
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		return zero, fmt.Errorf("%w: it did not finish rendering in time", ErrRenderLimit)
	}
}

// CheckOutput refuses what a render produced when it is too large.
func CheckOutput(out string) error {
	if len(out) > maxRenderOutput {
		return fmt.Errorf("%w: it renders to more than %d MB", ErrRenderLimit, maxRenderOutput>>20)
	}
	return nil
}

// largestTemplate is the size of the largest template of a chart and its
// subcharts.
func largestTemplate(ch *chart.Chart) int64 {
	var most int64 = 1
	for _, t := range ch.Templates {
		most = max(most, int64(len(t.Data)))
	}
	for _, d := range ch.Dependencies() {
		most = max(most, largestTemplate(d))
	}
	return most
}

// RenderFuncs replaces the template functions that generate loops and text
// with ones that spend a budget for one render, and says whether it ran
// out; each stops the render with an error when it does. The rest of what
// they do is sprig's.
func RenderFuncs(ch *chart.Chart) (template.FuncMap, func() bool) {
	left := atomic.Int64{}
	left.Store(renderBudget)
	var out atomic.Bool
	each := largestTemplate(ch)
	// spend takes n items of size bytes each.
	spend := func(n, size int64) error {
		if n < 0 {
			n = -n
		}
		if n == 0 || size == 0 {
			return nil
		}
		if n > math.MaxInt64/size || left.Add(-n*size) < 0 {
			out.Store(true)
			return fmt.Errorf("%w: it loops or repeats too much", ErrRenderLimit)
		}
		return nil
	}
	sp := sprig.TxtFuncMap()
	until := sp["until"].(func(int) []int)
	untilStep := sp["untilStep"].(func(int, int, int) []int)
	seq := sp["seq"].(func(...int) string)
	repeat := sp["repeat"].(func(int, string) string)
	rounds := func(start, stop, step int) int64 {
		if step == 0 {
			return 0
		}
		if step < 0 {
			step = -step
		}
		if stop < start {
			start, stop = stop, start
		}
		return (int64(stop)-int64(start))/int64(step) + 1
	}
	return template.FuncMap{
		"until": func(n int) ([]int, error) {
			if err := spend(int64(n), each); err != nil {
				return nil, err
			}
			return until(n), nil
		},
		"untilStep": func(start, stop, step int) ([]int, error) {
			if err := spend(rounds(start, stop, step), each); err != nil {
				return nil, err
			}
			return untilStep(start, stop, step), nil
		},
		"seq": func(params ...int) (string, error) {
			n := int64(0)
			switch len(params) {
			case 1:
				n = rounds(1, params[0], 1)
			case 2:
				n = rounds(params[0], params[1], 1)
			case 3:
				n = rounds(params[0], params[2], params[1])
			}
			if err := spend(n, each); err != nil {
				return "", err
			}
			return seq(params...), nil
		},
		"repeat": func(n int, s string) (string, error) {
			if n > 0 {
				if err := spend(int64(n), int64(len(s))); err != nil {
					return "", err
				}
			}
			return repeat(n, s), nil
		},
	}, out.Load
}
