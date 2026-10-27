package watson

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosuri/uitable"
	"helm.sh/helm/v4/pkg/action"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/cli/output"
	"helm.sh/helm/v4/pkg/kube"
	ri "helm.sh/helm/v4/pkg/release"
	release "helm.sh/helm/v4/pkg/release/v1"
	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	"k8s.io/kubectl/pkg/cmd/get"

	k8s "github.com/ucesys/221b/server/internal/kube"
)

// MaskValues hides the values under credential-looking keys, as the Helm
// tab does (P2-07).
func MaskValues(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	var masked []string
	out, _ := k8s.MaskValues(v, "", &masked).(map[string]any)
	return out
}

// Helm commands through the Helm SDK (P6-03). The output follows the helm
// CLI's own printers (helm.sh/helm/v4/pkg/cmd, Apache License 2.0), which
// the SDK does not export: the same columns, time formats and wording.

// Helm's changes are owned by the field manager the helm CLI uses, so a
// release installed with helm can be rolled back here, and the reverse,
// without conflicts over who manages a field.
func init() { kube.ManagedFieldsManager = "helm" }

// helmConfig prepares the SDK against the cluster, as the person.
func helmConfig(ctx context.Context, c *Command, t Target, errOut io.Writer, namespace string) (*action.Configuration, error) {
	g := newGetter(ctx, t, c.Namespace, errOut)
	cfg := action.NewConfiguration()
	cfg.SetLogger(slog.NewTextHandler(io.Discard, nil))
	if err := cfg.Init(g, namespace, "secret"); err != nil {
		return nil, err
	}
	return cfg, nil
}

func helmNamespace(c *Command) string {
	if c.Namespace == "" {
		return "default"
	}
	return c.Namespace
}

// runHelm runs one helm command and returns its exit code.
func runHelm(ctx context.Context, c *Command, t Target, out, errOut io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(errOut, "Error: %v\n", r)
			code = 1
		}
	}()
	if err := helm(ctx, c, t, out, errOut); err != nil {
		if ctx.Err() != nil {
			return 130
		}
		fmt.Fprintf(errOut, "Error: %v\n", err)
		return 1
	}
	return 0
}

func atoi(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return n, nil
}

func helm(ctx context.Context, c *Command, t Target, out, errOut io.Writer) error {
	ns := helmNamespace(c)
	format := output.Format(c.Flag("output"))
	if format == "" {
		format = output.Table
	}
	switch c.Name() {
	case "helm list":
		if c.Has("all-namespaces") {
			ns = ""
		}
		cfg, err := helmConfig(ctx, c, t, errOut, ns)
		if err != nil {
			return err
		}
		l := action.NewList(cfg)
		l.All, l.AllNamespaces = c.Has("all"), c.Has("all-namespaces")
		l.Deployed, l.Failed, l.Pending, l.Superseded, l.Uninstalled, l.Uninstalling = c.Has("deployed"), c.Has("failed"), c.Has("pending"), c.Has("superseded"), c.Has("uninstalled"), c.Has("uninstalling")
		l.Filter, l.Short, l.ByDate, l.SortReverse, l.NoHeaders, l.TimeFormat, l.Selector = c.Flag("filter"), c.Has("short"), c.Has("date"), c.Has("reverse"), c.Has("no-headers"), c.Flag("time-format"), c.Flag("selector")
		if l.Limit, err = atoi(c.Flag("max"), 256); err != nil {
			return err
		}
		if l.Offset, err = atoi(c.Flag("offset"), 0); err != nil {
			return err
		}
		l.SetStateMask()
		res, err := l.Run()
		if err != nil {
			return err
		}
		rels, err := v1List(res)
		if err != nil {
			return err
		}
		if l.Short {
			names := make([]string, 0, len(rels))
			for _, r := range rels {
				names = append(names, r.Name)
			}
			switch format {
			case output.JSON:
				return output.EncodeJSON(out, names)
			case output.YAML:
				return output.EncodeYAML(out, names)
			}
			for _, n := range names {
				fmt.Fprintln(out, n)
			}
			return nil
		}
		return format.Write(out, newListWriter(rels, l.TimeFormat, l.NoHeaders))
	case "helm status":
		cfg, err := helmConfig(ctx, c, t, errOut, ns)
		if err != nil {
			return err
		}
		s := action.NewStatus(cfg)
		if s.Version, err = atoi(c.Flag("revision"), 0); err != nil {
			return err
		}
		// As helm does: the resources as tables, read from the cluster, and
		// no chart.
		s.ShowResourcesTable = format == output.Table
		r, err := s.Run(c.Positionals[0])
		if err != nil {
			return err
		}
		rel, err := v1(r)
		if err != nil {
			return err
		}
		rel.Chart = nil
		return format.Write(out, statusPrinter{rel: rel})
	case "helm history":
		cfg, err := helmConfig(ctx, c, t, errOut, ns)
		if err != nil {
			return err
		}
		h := action.NewHistory(cfg)
		if h.Max, err = atoi(c.Flag("max"), 256); err != nil {
			return err
		}
		res, err := h.Run(c.Positionals[0])
		if err != nil {
			return err
		}
		rels, err := v1List(res)
		if err != nil {
			return err
		}
		releaseutil.Reverse(rels, releaseutil.SortByRevision)
		if len(rels) > h.Max {
			rels = rels[:h.Max]
		}
		var hist history
		for _, r := range slices.Backward(rels) {
			hist = append(hist, historyItem{Revision: r.Version, Updated: r.Info.LastDeployed, Status: r.Info.Status.String(), Chart: chartName(r.Chart), AppVersion: appVersion(r.Chart), Description: r.Info.Description})
		}
		if hist == nil {
			hist = history{}
		}
		return format.Write(out, hist)
	case "helm get values":
		cfg, err := helmConfig(ctx, c, t, errOut, ns)
		if err != nil {
			return err
		}
		g := action.NewGetValues(cfg)
		if g.Version, err = atoi(c.Flag("revision"), 0); err != nil {
			return err
		}
		g.AllValues = c.Has("all")
		vals, err := g.Run(c.Positionals[0])
		if err != nil {
			return err
		}
		return format.Write(out, valuesWriter{vals: MaskValues(vals), all: g.AllValues})
	case "helm rollback":
		cfg, err := helmConfig(ctx, c, t, errOut, ns)
		if err != nil {
			return err
		}
		r := action.NewRollback(cfg)
		r.WaitOptions = append(r.WaitOptions, kube.WithStatusComputeWorkers(4))
		if len(c.Positionals) > 1 {
			if r.Version, err = strconv.Atoi(c.Positionals[1]); err != nil {
				return fmt.Errorf("could not convert revision to a number: %w", err)
			}
		}
		r.Timeout = 300 * time.Second
		if v := c.Flag("timeout"); v != "" {
			if r.Timeout, err = time.ParseDuration(v); err != nil {
				return fmt.Errorf("invalid --timeout %q: %w", v, err)
			}
		}
		r.WaitStrategy = kube.HookOnlyStrategy
		if c.Has("wait") {
			switch w := c.Flag("wait"); w {
			case "", "true", "watcher":
				r.WaitStrategy = kube.StatusWatcherStrategy
			case "false", "hookOnly":
				r.WaitStrategy = kube.HookOnlyStrategy
			case "legacy":
				r.WaitStrategy = kube.LegacyStrategy
			}
		}
		r.DisableHooks, r.CleanupOnFail, r.WaitForJobs = c.Has("no-hooks"), c.Has("cleanup-on-fail"), c.Has("wait-for-jobs")
		if r.MaxHistory, err = atoi(c.Flag("history-max"), 10); err != nil {
			return err
		}
		switch c.Flag("dry-run") {
		case "true", "client":
			r.DryRunStrategy = action.DryRunClient
		case "server":
			r.DryRunStrategy = action.DryRunServer
		}
		if err := r.Run(c.Positionals[0]); err != nil {
			return err
		}
		fmt.Fprint(out, "Rollback was a success! Happy Helming!\n")
		return nil
	case "helm install", "helm upgrade", "helm uninstall":
		return helmApp(ctx, c, t, out, ns)
	}
	return fmt.Errorf("%s is not supported", c.Name())
}

func v1(r ri.Releaser) (*release.Release, error) {
	switch x := r.(type) {
	case release.Release:
		return &x, nil
	case *release.Release:
		return x, nil
	case nil:
		return nil, fmt.Errorf("release not found")
	}
	return nil, fmt.Errorf("unsupported release type: %T", r)
}

func v1List(ls []ri.Releaser) ([]*release.Release, error) {
	out := make([]*release.Release, 0, len(ls))
	for _, r := range ls {
		rel, err := v1(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, nil
}

func chartName(c *chart.Chart) string {
	if c == nil || c.Metadata == nil {
		return "MISSING"
	}
	return fmt.Sprintf("%s-%s", c.Name(), c.Metadata.Version)
}

func appVersion(c *chart.Chart) string {
	if c == nil || c.Metadata == nil {
		return "MISSING"
	}
	return c.AppVersion()
}

// ---------- helm list ----------

type listElement struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Revision   string `json:"revision"`
	Updated    string `json:"updated"`
	Status     string `json:"status"`
	Chart      string `json:"chart"`
	AppVersion string `json:"app_version"`
}

type listWriter struct {
	releases  []listElement
	noHeaders bool
}

func newListWriter(rels []*release.Release, timeFormat string, noHeaders bool) *listWriter {
	els := make([]listElement, 0, len(rels))
	for _, r := range rels {
		el := listElement{Name: r.Name, Namespace: r.Namespace, Revision: strconv.Itoa(r.Version), Status: r.Info.Status.String(), Chart: chartName(r.Chart), AppVersion: appVersion(r.Chart), Updated: "-"}
		if t := r.Info.LastDeployed; !t.IsZero() {
			if timeFormat != "" {
				el.Updated = t.Format(timeFormat)
			} else {
				el.Updated = t.String()
			}
		}
		els = append(els, el)
	}
	return &listWriter{els, noHeaders}
}

func (w *listWriter) WriteTable(out io.Writer) error {
	t := uitable.New()
	if !w.noHeaders {
		t.AddRow("NAME", "NAMESPACE", "REVISION", "UPDATED", "STATUS", "CHART", "APP VERSION")
	}
	for _, r := range w.releases {
		t.AddRow(r.Name, r.Namespace, r.Revision, r.Updated, r.Status, r.Chart, r.AppVersion)
	}
	return output.EncodeTable(out, t)
}
func (w *listWriter) WriteJSON(out io.Writer) error { return output.EncodeJSON(out, w.releases) }
func (w *listWriter) WriteYAML(out io.Writer) error { return output.EncodeYAML(out, w.releases) }

// ---------- helm history ----------

type historyItem struct {
	Revision    int       `json:"revision"`
	Updated     time.Time `json:"updated,omitzero"`
	Status      string    `json:"status"`
	Chart       string    `json:"chart"`
	AppVersion  string    `json:"app_version"`
	Description string    `json:"description"`
}

type history []historyItem

func (h history) WriteJSON(out io.Writer) error { return output.EncodeJSON(out, h) }
func (h history) WriteYAML(out io.Writer) error { return output.EncodeYAML(out, h) }
func (h history) WriteTable(out io.Writer) error {
	t := uitable.New()
	t.AddRow("REVISION", "UPDATED", "STATUS", "CHART", "APP VERSION", "DESCRIPTION")
	for _, it := range h {
		t.AddRow(it.Revision, it.Updated.Format(time.ANSIC), it.Status, it.Chart, it.AppVersion, it.Description)
	}
	return output.EncodeTable(out, t)
}

// ---------- helm get values ----------

type valuesWriter struct {
	vals map[string]any
	all  bool
}

func (v valuesWriter) WriteTable(out io.Writer) error {
	if v.all {
		fmt.Fprintln(out, "COMPUTED VALUES:")
	} else {
		fmt.Fprintln(out, "USER-SUPPLIED VALUES:")
	}
	return output.EncodeYAML(out, v.vals)
}
func (v valuesWriter) WriteJSON(out io.Writer) error { return output.EncodeJSON(out, v.vals) }
func (v valuesWriter) WriteYAML(out io.Writer) error { return output.EncodeYAML(out, v.vals) }

// ---------- helm status ----------

type statusPrinter struct {
	rel          *release.Release
	showMetadata bool
}

func (s statusPrinter) WriteJSON(out io.Writer) error { return output.EncodeJSON(out, s.redacted()) }
func (s statusPrinter) WriteYAML(out io.Writer) error { return output.EncodeYAML(out, s.redacted()) }

// redacted is the release without the values it was installed with,
// which may hold credentials: helm status -o json prints them, Watson
// shows them only masked, through helm get values.
func (s statusPrinter) redacted() *release.Release {
	r := *s.rel
	r.Config = MaskValues(r.Config)
	// The rendered manifest and hooks can hold Secrets.
	r.Manifest = "(hidden by 221B: the rendered manifest can hold Secrets)"
	r.Hooks = nil
	if r.Chart != nil {
		ch := *r.Chart
		ch.Values = MaskValues(ch.Values)
		r.Chart = &ch
	}
	return &r
}

func (s statusPrinter) WriteTable(out io.Writer) error {
	rel := s.rel
	fmt.Fprintf(out, "NAME: %s\n", rel.Name)
	if !rel.Info.LastDeployed.IsZero() {
		fmt.Fprintf(out, "LAST DEPLOYED: %s\n", rel.Info.LastDeployed.Format(time.ANSIC))
	}
	fmt.Fprintf(out, "NAMESPACE: %s\n", rel.Namespace)
	fmt.Fprintf(out, "STATUS: %s\n", rel.Info.Status)
	fmt.Fprintf(out, "REVISION: %d\n", rel.Version)
	if s.showMetadata && rel.Chart != nil && rel.Chart.Metadata != nil {
		fmt.Fprintf(out, "CHART: %s\n", rel.Chart.Metadata.Name)
		fmt.Fprintf(out, "VERSION: %s\n", rel.Chart.Metadata.Version)
		fmt.Fprintf(out, "APP_VERSION: %s\n", rel.Chart.Metadata.AppVersion)
	}
	fmt.Fprintf(out, "DESCRIPTION: %s\n", rel.Info.Description)
	if len(rel.Info.Resources) > 0 {
		buf := new(bytes.Buffer)
		printFlags := get.NewHumanPrintFlags()
		typePrinter, _ := printFlags.ToPrinter("")
		printer := &get.TablePrinter{Delegate: typePrinter}
		var keys []string
		for key := range rel.Info.Resources {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, t := range keys {
			fmt.Fprintf(buf, "==> %s\n", t)
			for _, res := range rel.Info.Resources[t] {
				if err := printer.PrintObj(res, buf); err != nil {
					fmt.Fprintf(buf, "failed to print object type %s: %v\n", t, err)
				}
			}
			buf.WriteString("\n")
		}
		fmt.Fprintf(out, "RESOURCES:\n%s\n", buf.String())
	}
	tests := false
	for _, h := range rel.Hooks {
		for _, e := range h.Events {
			if e == release.HookTest && !h.LastRun.StartedAt.IsZero() {
				tests = true
				fmt.Fprintf(out, "TEST SUITE:     %s\n%s\n%s\n%s\n", h.Name, "Last Started:   "+h.LastRun.StartedAt.Format(time.ANSIC), "Last Completed: "+h.LastRun.CompletedAt.Format(time.ANSIC), "Phase:          "+h.LastRun.Phase)
			}
		}
	}
	if !tests {
		fmt.Fprintln(out, "TEST SUITE: None")
	}
	if rel.Info.Notes != "" {
		fmt.Fprintf(out, "NOTES:\n%s\n", strings.TrimSpace(rel.Info.Notes))
	}
	return nil
}
