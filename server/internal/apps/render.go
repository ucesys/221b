package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/go-git/go-billy/v5/util"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	rcommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
	"helm.sh/helm/v4/pkg/strvals"
	"sigs.k8s.io/yaml"
)

// Values builds an application's values from its values file and its
// parameters, the parameters last, as helm -f values.yaml --set k=v does
// (HA-12, HA-13).
func Values(valuesYAML string, params []string) (map[string]any, error) {
	vals := map[string]any{}
	if strings.TrimSpace(valuesYAML) != "" {
		v, err := loader.LoadValues(strings.NewReader(valuesYAML))
		if err != nil {
			return nil, fmt.Errorf("the values file is not valid YAML: %w", err)
		}
		vals = v
	}
	for _, p := range params {
		if err := strvals.ParseInto(p, vals); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p, err)
		}
	}
	return vals, nil
}

// Compose loads the chart at the source's path and builds the values an
// application deploys with, lowest first: the chart's values.yaml (left
// out with IgnoreDefaults), the record's second values file read from the
// checkout, the record's Values, then its Params and extra, and the
// fleet's deploy label as the pods' nodeSelector (R2-09, R2-11). A nil
// record composes the source alone.
func Compose(c *Checkout, src Source, rec *Record, extra []string) (*chart.Chart, map[string]any, error) {
	ch, err := LoadChart(c, src.Path)
	if err != nil {
		return nil, nil, err
	}
	var file, values string
	var params []string
	if rec != nil {
		values, params = rec.Values, append([]string(nil), rec.Params...)
		if rec.IgnoreDefaults {
			ch.Values = map[string]any{}
		}
		if rec.ValuesFile != "" {
			name := path.Clean("/" + rec.ValuesFile)
			b, err := util.ReadFile(c.FS, path.Join("/", src.Path, name))
			if err != nil {
				return nil, nil, fmt.Errorf("the values file %s is not in the chart's directory at this ref", rec.ValuesFile)
			}
			file = string(b)
		}
		if k, v, ok := strings.Cut(rec.FleetLabel, "="); ok && k != "" {
			params = append(params, "nodeSelector."+strings.ReplaceAll(k, ".", `\.`)+"="+v)
		}
	}
	params = append(params, extra...)
	vals, err := ValuesFrom(file, values, params)
	if err != nil {
		return nil, nil, err
	}
	if err := CheckDependencies(ch, vals); err != nil {
		return nil, nil, err
	}
	return ch, vals, nil
}

// ValuesFrom builds the values from a values file, the values typed in
// the console over it, and the parameters last, as helm -f file -f values
// --set k=v does.
func ValuesFrom(fileYAML, valuesYAML string, params []string) (map[string]any, error) {
	base := map[string]any{}
	if strings.TrimSpace(fileYAML) != "" {
		v, err := loader.LoadValues(strings.NewReader(fileYAML))
		if err != nil {
			return nil, fmt.Errorf("the second values file is not valid YAML: %w", err)
		}
		base = v
	}
	over, err := Values(valuesYAML, nil)
	if err != nil {
		return nil, err
	}
	vals := mergeMaps(base, over)
	for _, p := range params {
		if err := strvals.ParseInto(p, vals); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p, err)
		}
	}
	return vals, nil
}

// mergeMaps lays b over a, table by table, as helm merges its -f files.
func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if bt, ok := v.(map[string]any); ok {
			if at, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(at, bt)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// ValuesYAML is the values as YAML, for showing.
func ValuesYAML(vals map[string]any) string {
	if len(vals) == 0 {
		return ""
	}
	b, err := yaml.Marshal(vals)
	if err != nil {
		return ""
	}
	return string(b)
}

// Rendered is a chart rendered for a release: what would be applied.
type Rendered struct {
	Manifest string
	Notes    string
}

// Render renders the chart for the release on the console alone, as helm
// template does (HA-17): no cluster is asked, so it is what the source
// says, for comparing with what is deployed. It renders in a process of
// its own, within the limits of limits.go.
func Render(ctx context.Context, ch *chart.Chart, vals map[string]any, name, namespace, kubeVersion string) (Rendered, error) {
	return RenderRelease(ctx, ch, vals, ReleaseOptions{Name: name, Namespace: namespace, KubeVersion: kubeVersion})
}

// RenderRelease is Render for a release described in full: an upgrade, the
// cluster's API versions, Secrets left out. Lookups find nothing.
func RenderRelease(ctx context.Context, ch *chart.Chart, vals map[string]any, o ReleaseOptions) (Rendered, error) {
	return renderOutside(ctx, ch, vals, o)
}

// renderHere renders in this process: the child's side of RenderRelease.
func renderHere(ctx context.Context, ch *chart.Chart, vals map[string]any, o ReleaseOptions) (out Rendered, err error) {
	funcs, tripped := RenderFuncs(ch)
	defer func() {
		if r := recover(); r != nil {
			out, err = Rendered{}, fmt.Errorf("rendering the chart: %v", r)
		}
		if tripped() {
			out, err = Rendered{}, fmt.Errorf("%w: it loops or repeats too much", ErrRenderLimit)
		}
	}()
	cfg := action.NewConfiguration()
	cfg.Releases = storage.Init(driver.NewMemory())
	cfg.KubeClient = &kubefake.PrintingKubeClient{Out: io.Discard}
	cfg.SetLogger(slog.NewTextHandler(io.Discard, nil))
	cfg.CustomTemplateFuncs = funcs
	caps := *common.DefaultCapabilities
	if o.KubeVersion != "" {
		if v, err := common.ParseKubeVersion(o.KubeVersion); err == nil {
			caps.KubeVersion = *v
		}
	}
	if len(o.APIVersions) > 0 {
		caps.APIVersions = common.VersionSet(o.APIVersions)
	}
	cfg.Capabilities = &caps
	var rel any
	if o.Upgrade {
		// The release deployed now, as far as a render needs it: its
		// revision and the values it was deployed with.
		prev := &release.Release{Name: o.Name, Namespace: o.Namespace, Version: max(o.Revision-1, 1), Config: o.Previous,
			Chart: &chart.Chart{Metadata: ch.Metadata}, Info: &release.Info{Status: rcommon.StatusDeployed}}
		if err := cfg.Releases.Create(prev); err != nil {
			return Rendered{}, fmt.Errorf("rendering the chart: %w", err)
		}
		u := action.NewUpgrade(cfg)
		u.Namespace = o.Namespace
		u.DryRunStrategy, u.HideSecret = action.DryRunClient, o.HideSecret
		u.DisableOpenAPIValidation = true
		rel, err = u.RunWithContext(ctx, o.Name, ch, vals)
	} else {
		i := action.NewInstall(cfg)
		i.DryRunStrategy, i.HideSecret = action.DryRunClient, o.HideSecret
		i.ReleaseName, i.Namespace = o.Name, o.Namespace
		i.Replace, i.IncludeCRDs = true, true
		// An install on the console starts from Helm's capabilities and
		// takes the cluster's from these.
		i.KubeVersion, i.APIVersions = &caps.KubeVersion, caps.APIVersions
		rel, err = i.RunWithContext(ctx, ch, vals)
	}
	if err != nil {
		return Rendered{}, fmt.Errorf("rendering the chart: %w", err)
	}
	r, err := V1(rel)
	if err != nil {
		return Rendered{}, err
	}
	if err := CheckOutput(r.Manifest + r.Info.Notes); err != nil {
		return Rendered{}, err
	}
	return Rendered{Manifest: r.Manifest, Notes: r.Info.Notes}, nil
}

// V1 is a release in the format the SDK's v1 types use.
func V1(r any) (*release.Release, error) {
	if r == nil {
		return nil, errors.New("helm returned no release")
	}
	if rel, ok := r.(*release.Release); ok {
		return rel, nil
	}
	if rel, ok := r.(release.Release); ok {
		return &rel, nil
	}
	return nil, fmt.Errorf("helm returned a release of an unexpected kind (%T)", r)
}
