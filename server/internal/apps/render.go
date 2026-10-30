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
// says, for comparing with what is deployed.
func Render(ctx context.Context, ch *chart.Chart, vals map[string]any, name, namespace, kubeVersion string) (Rendered, error) {
	cfg := action.NewConfiguration()
	cfg.Releases = storage.Init(driver.NewMemory())
	cfg.KubeClient = &kubefake.PrintingKubeClient{Out: io.Discard}
	cfg.SetLogger(slog.NewTextHandler(io.Discard, nil))
	caps := *common.DefaultCapabilities
	if kubeVersion != "" {
		if v, err := common.ParseKubeVersion(kubeVersion); err == nil {
			caps.KubeVersion = *v
		}
	}
	cfg.Capabilities = &caps
	i := action.NewInstall(cfg)
	i.DryRunStrategy = action.DryRunClient
	i.ReleaseName, i.Namespace = name, namespace
	i.Replace, i.IncludeCRDs = true, true
	rel, err := i.RunWithContext(ctx, ch, vals)
	if err != nil {
		return Rendered{}, fmt.Errorf("rendering the chart: %w", err)
	}
	r, err := V1(rel)
	if err != nil {
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
