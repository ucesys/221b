package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

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
