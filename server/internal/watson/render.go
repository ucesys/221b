package watson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/action"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	rcommon "helm.sh/helm/v4/pkg/release/common"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/ucesys/221b/server/internal/apps"
)

// clusterCaps are the cluster's version and API versions, as Helm asks
// them, or nothing when discovery fails: the render then takes Helm's
// defaults.
func clusterCaps(cfg *action.Configuration) (string, []string) {
	if cfg.RESTClientGetter == nil {
		return "", nil
	}
	dc, err := cfg.RESTClientGetter.ToDiscoveryClient()
	if err != nil {
		return "", nil
	}
	v, err := dc.ServerVersion()
	if err != nil {
		return "", nil
	}
	set, _ := action.GetVersionSet(dc)
	return v.GitVersion, set
}

// upgradeOptions describe an upgrade of the release for a render on the
// console: the next revision, the values the release runs with, and the
// cluster's capabilities. The release is read through cfg, as the person.
func upgradeOptions(cfg *action.Configuration, ns, name string) (apps.ReleaseOptions, error) {
	last, err := cfg.Releases.Last(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return apps.ReleaseOptions{}, driver.NewErrNoDeployedReleases(name)
	}
	if err != nil {
		return apps.ReleaseOptions{}, err
	}
	rel, err := v1(last)
	if err != nil {
		return apps.ReleaseOptions{}, err
	}
	o := apps.ReleaseOptions{Name: name, Namespace: ns, Upgrade: true, Revision: rel.Version + 1, Previous: rel.Config}
	o.KubeVersion, o.APIVersions = clusterCaps(cfg)
	return o, nil
}

// dryRun renders an install or upgrade for its dry run and prints it as
// helm does. The values that look like credentials are masked first, the
// chart renders on the console in a process of its own, where lookup
// finds nothing, and Secret objects are left out.
func (a *appChart) dryRun(ctx context.Context, cfg *action.Configuration, out io.Writer, ch *chart.Chart, ns, name string, install bool) error {
	o := apps.ReleaseOptions{Name: name, Namespace: ns}
	status := rcommon.StatusPendingInstall
	if install {
		o.Revision = 1
		o.KubeVersion, o.APIVersions = clusterCaps(cfg)
	} else {
		var err error
		if o, err = upgradeOptions(cfg, ns, name); err != nil {
			return err
		}
		o.Previous = MaskValues(o.Previous)
		status = rcommon.StatusPendingUpgrade
	}
	o.HideSecret = true
	vals := MaskValues(a.vals)
	r, err := apps.RenderRelease(ctx, ch, vals, o)
	if err != nil {
		return err
	}
	rel := &release.Release{Name: name, Namespace: ns, Version: o.Revision, Chart: ch, Config: vals, Manifest: r.Manifest,
		Info: &release.Info{Status: status, Notes: r.Notes, LastDeployed: time.Now(), Description: "Dry run complete"}}
	fmt.Fprintf(out, "DRY RUN: %s %s from %s at %s %s\n", map[bool]string{true: "install", false: "upgrade"}[install], name, a.src, commitWord(a.co.Commit), short(a.co.Commit))
	fmt.Fprintln(out, "# watson: rendered without reading the cluster: lookup finds nothing here; Secrets are left out and credentials masked")
	if err := (statusPrinter{rel: rel, showMetadata: true}).WriteTable(out); err != nil {
		return err
	}
	fmt.Fprintf(out, "MANIFEST:\n%s\n", strings.TrimSpace(rel.Manifest))
	return nil
}
