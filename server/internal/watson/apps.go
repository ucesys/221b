package watson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/action"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/kube"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/ucesys/221b/server/internal/apps"
)

// Helm applications (Phase 10, HA-06 to HA-08): helm install, upgrade and
// uninstall of a chart fetched from a git repository, as the person. The
// line names the source (--git, --path, --ref, --tag); --app takes the
// values file and parameters the console keeps for the application, and
// the record is brought up to date when the change succeeds.

// appSource reads the source from the line's flags.
func appSource(c *Command) (apps.Source, bool, error) {
	if !c.Has("git") {
		return apps.Source{}, false, nil
	}
	s := apps.Source{Repo: c.Flag("git"), Path: c.Flag("path"), Ref: c.Flag("ref"), RefKind: apps.RefBranch}
	if c.Has("tag") {
		s.RefKind = apps.RefTag
	}
	s, err := apps.Normalize(s)
	return s, true, err
}

// appChart fetches and loads the chart a command names, with the values
// it asks for. The record, when the command uses one, supplies the
// source, the token, the values file and the parameters.
type appChart struct {
	src    apps.Source
	rec    *apps.Record
	co     *apps.Checkout
	ch     *chart.Chart
	vals   map[string]any
	values string
	params []string
}

func loadAppChart(ctx context.Context, c *Command, t Target, ns, name string) (*appChart, error) {
	src, given, err := appSource(c)
	if err != nil {
		return nil, err
	}
	a := &appChart{src: src}
	token := ""
	if c.Has("app") {
		if t.Apps == nil {
			return nil, errors.New("this console keeps no applications")
		}
		rec, tok, err := t.Apps.Get(ctx, t.Cluster, ns, name)
		switch {
		case errors.Is(err, apps.ErrNotFound):
			if !given {
				return nil, fmt.Errorf("no application %s/%s is kept: name its source with --git", ns, name)
			}
		case err != nil:
			return nil, err
		default:
			a.rec, token = &rec, tok
			if !given {
				a.src = rec.Source
			}
			a.values, a.params = rec.Values, rec.Params
		}
	}
	if a.src.Repo == "" {
		return nil, errors.New("name the chart's repository with --git, and its branch or tag with --ref")
	}
	for _, set := range c.Flags["set"] {
		a.params = append(a.params, set)
	}
	if a.vals, err = apps.Values(a.values, a.params); err != nil {
		return nil, err
	}
	if a.co, err = apps.Fetch(ctx, a.src, token); err != nil {
		return nil, err
	}
	// The record's second values file, its fleet and whether the chart's
	// own values.yaml counts need the checkout (R2-08, R2-09, R2-11).
	if a.rec != nil && (a.rec.ValuesFile != "" || a.rec.IgnoreDefaults || a.rec.FleetLabel != "") {
		if a.ch, a.vals, err = apps.Compose(a.co, a.src, a.rec, c.Flags["set"]); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *appChart) load() (*chart.Chart, error) {
	if a.ch != nil {
		return a.ch, nil
	}
	return apps.LoadChart(a.co, a.src.Path)
}

// helmApp runs helm install, upgrade or uninstall.
func helmApp(ctx context.Context, c *Command, t Target, out io.Writer, ns string) error {
	name := c.Positionals[0]
	dry := c.Has("dry-run") && c.Flag("dry-run") != "false" && c.Flag("dry-run") != "none"
	strategy := action.DryRunNone
	if dry {
		strategy = action.DryRunServer
		if v := c.Flag("dry-run"); v == "client" {
			strategy = action.DryRunClient
		}
	}
	timeout := 300 * time.Second
	if v := c.Flag("timeout"); v != "" {
		var err error
		if timeout, err = time.ParseDuration(v); err != nil {
			return fmt.Errorf("invalid --timeout %q: %w", v, err)
		}
	}
	wait := kube.HookOnlyStrategy
	if c.Has("wait") {
		switch c.Flag("wait") {
		case "", "true", "watcher":
			wait = kube.StatusWatcherStrategy
		case "legacy":
			wait = kube.LegacyStrategy
		}
	}
	cfg, err := helmConfig(ctx, c, t, io.Discard, ns)
	if err != nil {
		return err
	}
	if c.Name() == "helm uninstall" {
		u := action.NewUninstall(cfg)
		u.KeepHistory, u.DryRun, u.Timeout, u.WaitStrategy = c.Has("keep-history"), dry, timeout, wait
		u.DisableHooks = c.Has("no-hooks")
		res, err := u.Run(name)
		if err != nil {
			return err
		}
		if dry {
			fmt.Fprintf(out, "DRY RUN: release %q would be uninstalled\n", name)
			return nil
		}
		if res != nil && res.Info != "" {
			fmt.Fprintln(out, res.Info)
		}
		fmt.Fprintf(out, "release %q uninstalled\n", name)
		if t.Apps != nil {
			if err := t.Apps.Delete(ctx, t.Cluster, ns, name); err != nil {
				fmt.Fprintf(out, "# watson: the application's record was not removed: %v\n", err)
			}
		}
		return nil
	}

	a, err := loadAppChart(ctx, c, t, ns, name)
	if err != nil {
		return err
	}
	ch, err := a.load()
	if err != nil {
		return err
	}
	desc := "221B: " + a.src.String() + " (" + short(a.co.Commit) + ")"
	install := c.Name() == "helm install"
	if !install && c.Has("install") {
		// helm upgrade --install: an install when there is no release yet.
		h := action.NewHistory(cfg)
		h.Max = 1
		if _, err := h.Run(name); errors.Is(err, driver.ErrReleaseNotFound) {
			install = true
		}
	}
	var rel *release.Release
	if install {
		i := action.NewInstall(cfg)
		i.ReleaseName, i.Namespace, i.CreateNamespace = name, ns, c.Has("create-namespace")
		i.DryRunStrategy, i.HideSecret = strategy, dry
		i.Timeout, i.WaitStrategy, i.DisableHooks = timeout, wait, c.Has("no-hooks")
		i.WaitOptions = append(i.WaitOptions, kube.WithStatusComputeWorkers(4))
		// The API server validates what it is sent, and says what is wrong;
		// the SDK's own check needs the OpenAPI document, which the
		// simulator does not serve.
		i.DisableOpenAPIValidation = true
		// Objects are created and updated as the helm CLI did before
		// server-side apply, with "helm" as the field manager (helm.go), so
		// the CLI and the console can take over from each other; an
		// upgrade follows what the release used ("auto").
		i.ServerSideApply = false
		i.Description = desc
		r, err := i.RunWithContext(ctx, ch, a.vals)
		if err != nil {
			return err
		}
		if rel, err = v1(r); err != nil {
			return err
		}
	} else {
		u := action.NewUpgrade(cfg)
		u.Namespace = ns
		u.DryRunStrategy, u.HideSecret = strategy, dry
		u.Timeout, u.WaitStrategy, u.DisableHooks = timeout, wait, c.Has("no-hooks")
		u.WaitOptions = append(u.WaitOptions, kube.WithStatusComputeWorkers(4))
		u.DisableOpenAPIValidation = true
		u.MaxHistory, u.Description = 10, desc
		r, err := u.RunWithContext(ctx, name, ch, a.vals)
		if err != nil {
			return err
		}
		if rel, err = v1(r); err != nil {
			return err
		}
	}
	if dry {
		fmt.Fprintf(out, "DRY RUN: %s %s from %s at commit %s\n", map[bool]string{true: "install", false: "upgrade"}[install], name, a.src, short(a.co.Commit))
	}
	if err := (statusPrinter{rel: rel, showMetadata: true}).WriteTable(out); err != nil {
		return err
	}
	if dry {
		fmt.Fprintf(out, "MANIFEST:\n%s\n", strings.TrimSpace(rel.Manifest))
		return nil
	}
	if t.Apps != nil {
		now := time.Now().UTC()
		rec := apps.Record{Cluster: t.Cluster, Namespace: ns, Name: name, Created: now, CreatedBy: t.Person.User}
		if a.rec != nil {
			rec = *a.rec
		}
		rec.Source, rec.Values, rec.Params = a.src, a.values, a.params
		rec.Commit, rec.CommitDate, rec.CommitSubject = a.co.Commit, a.co.CommitDate, a.co.CommitSubject
		rec.ChartVersion, rec.Updated, rec.UpdatedBy = ch.Metadata.Version, now, t.Person.User
		if err := t.Apps.Save(ctx, rec, nil); err != nil {
			fmt.Fprintf(out, "# watson: the application's record was not saved: %v\n", err)
		}
	}
	return nil
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// appImpact is the card of an install, upgrade or uninstall: the chart
// and commit it would deploy, and the objects it creates, changes or
// deletes, from a render on the console (HA-07).
func appImpact(ctx context.Context, cs *clients, c *Command, t Target) (targets []string, guards []guard, impact []string) {
	ns := helmNamespace(c)
	name := c.Positionals[0]
	targets = []string{"release " + ns + "/" + name}
	read := func(ctx context.Context, cs *clients) ([]*release.Release, error) {
		d := driver.NewSecrets(cs.cs.CoreV1().Secrets(ns))
		res, err := d.Query(map[string]string{"name": name, "owner": "helm"})
		if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
			return nil, err
		}
		rels, err := v1List(res)
		if err != nil {
			return nil, err
		}
		sort.Slice(rels, func(i, j int) bool { return rels[i].Version > rels[j].Version })
		return rels, nil
	}
	rels, err := read(ctx, cs)
	if err != nil {
		return targets, nil, []string{"Watson could not read the release: " + err.Error()}
	}
	var cur *release.Release
	if len(rels) > 0 {
		cur = rels[0]
	}
	state := func(rels []*release.Release) string {
		if len(rels) == 0 {
			return "absent"
		}
		return digest(rels[0].Version, rels[0].Info.Status.String())
	}
	guards = append(guards, guard{what: "release " + ns + "/" + name, want: state(rels), read: func(ctx context.Context, cs *clients) (string, error) {
		rels, err := read(ctx, cs)
		if err != nil {
			return "", err
		}
		return state(rels), nil
	}})

	if c.Name() == "helm uninstall" {
		if cur == nil {
			return targets, guards, []string{fmt.Sprintf("There is no release %s in %s: helm will refuse.", name, ns)}
		}
		objs := objectNames(cur.Manifest)
		impact = append(impact, fmt.Sprintf("Uninstalls release %s/%s (%s %s, revision %d): deletes %s: %s.", ns, name, chartName(cur.Chart), cur.Chart.Metadata.Version, cur.Version, plural(len(objs), "object", "objects"), listText(objs, 8)))
		if c.Has("keep-history") {
			impact = append(impact, "The release's history is kept: helm history still lists it.")
		} else {
			impact = append(impact, "The release's history is removed with it; the console forgets the application.")
		}
		return targets, guards, impact
	}

	a, err := loadAppChart(ctx, c, t, ns, name)
	if err != nil {
		return targets, guards, []string{"Watson could not fetch the chart: " + err.Error()}
	}
	ch, err := a.load()
	if err != nil {
		return targets, guards, []string{"Watson could not load the chart: " + err.Error()}
	}
	r, err := apps.Render(ctx, ch, a.vals, name, ns, "")
	if err != nil {
		return targets, guards, []string{"Watson could not render the chart: " + err.Error()}
	}
	when := ""
	if !a.co.CommitDate.IsZero() {
		when = ", " + a.co.CommitDate.Format("Jan 2 2006")
	}
	from := fmt.Sprintf("%s at commit %s (%q%s)", a.src, short(a.co.Commit), a.co.CommitSubject, when)
	install := c.Name() == "helm install" || (c.Has("install") && cur == nil)
	switch {
	case install && cur != nil:
		impact = append(impact, fmt.Sprintf("Release %s/%s exists already (revision %d): helm will refuse. Upgrade it instead.", ns, name, cur.Version))
	case install:
		objs := objectNames(r.Manifest)
		impact = append(impact, fmt.Sprintf("Installs chart %s %s (app %s) from %s: creates %s: %s.", ch.Metadata.Name, ch.Metadata.Version, ch.Metadata.AppVersion, from, plural(len(objs), "object", "objects"), listText(objs, 8)))
	case cur == nil:
		impact = append(impact, fmt.Sprintf("There is no release %s in %s: helm will refuse. Install it, or add --install.", name, ns))
	default:
		impact = append(impact, fmt.Sprintf("Upgrades release %s/%s from %s %s (revision %d, %s) to %s %s (app %s) from %s. Helm records it as revision %d.",
			ns, name, chartName(cur.Chart), cur.Chart.Metadata.Version, cur.Version, cur.Info.Status, ch.Metadata.Name, ch.Metadata.Version, ch.Metadata.AppVersion, from, cur.Version+1))
		changed, added, removed := manifestDiff(cur.Manifest, r.Manifest)
		if len(changed)+len(added)+len(removed) == 0 {
			impact = append(impact, "The objects are the same as deployed: Helm applies them again and records the new revision.")
		}
		if len(changed) > 0 {
			impact = append(impact, fmt.Sprintf("Changes %s: %s.", plural(len(changed), "object", "objects"), listText(changed, 6)))
		}
		if len(added) > 0 {
			impact = append(impact, fmt.Sprintf("Creates %s: %s.", plural(len(added), "object", "objects"), listText(added, 6)))
		}
		if len(removed) > 0 {
			impact = append(impact, fmt.Sprintf("Deletes %s: %s.", plural(len(removed), "object", "objects"), listText(removed, 6)))
		}
	}
	if imgs := apps.Images(r.Manifest); len(imgs) > 0 {
		seen := map[string]bool{}
		var names []string
		for _, im := range imgs {
			if !seen[im.Image] {
				seen[im.Image] = true
				names = append(names, im.Image)
			}
		}
		impact = append(impact, fmt.Sprintf("Runs %s: %s.", plural(len(names), "image", "images"), listText(names, 6)))
	}
	return targets, guards, impact
}

// objectNames lists a manifest's objects as "Kind name".
func objectNames(manifest string) []string {
	changed, added, _ := manifestDiff("", manifest)
	return append(changed, added...)
}

// RenderUpgrade renders what an upgrade of the release from the chart
// would apply, as the person, with a server dry run (HA-17): the chart's
// lookups, the cluster's capabilities and the next revision are the real
// ones, which a render on the console alone cannot give.
func (s *Service) RenderUpgrade(ctx context.Context, cl Cluster, who Who, ns, name string, ch *chart.Chart, vals map[string]any) (string, error) {
	t := Target{Config: cl.Config, Person: who.Person, Cluster: cl.ID, Apps: s.opts.Apps}
	d, err := s.disc.For(cl.ID, cl.Config)
	if err != nil {
		return "", err
	}
	t.Discovery = d
	cfg, err := helmConfig(ctx, &Command{Namespace: ns}, t, io.Discard, ns)
	if err != nil {
		return "", err
	}
	u := action.NewUpgrade(cfg)
	u.Namespace = ns
	u.DryRunStrategy = action.DryRunServer
	u.DisableOpenAPIValidation = true
	u.Timeout = 60 * time.Second
	r, err := u.RunWithContext(ctx, name, ch, vals)
	if err != nil {
		return "", err
	}
	rel, err := v1(r)
	if err != nil {
		return "", err
	}
	return rel.Manifest, nil
}
