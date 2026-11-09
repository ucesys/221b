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
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/ucesys/221b/server/internal/apps"
)

// Helm applications (Phase 10, HA-06 to HA-08): helm install, upgrade and
// uninstall of a chart fetched from a git repository, as the person. The
// line names the source (--git, --path, --ref, --tag); --app takes the
// values file and parameters the console keeps for the application, and
// the record is brought up to date when the change succeeds: its commit
// and chart version, and the source the line named. Nothing else on the
// record changes, --set on the line included, which applies to that deploy.

// appSource reads the source from the line: its --git flags, or the
// repository chart after the release (Phase 12, wave 5, HR-07).
func appSource(c *Command) (apps.Source, bool, error) {
	if len(c.Positionals) > 1 {
		s, err := apps.ParseChartRef(c.Positionals[1], c.Flag("version"))
		return s, true, err
	}
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

// appPin is what an install or upgrade card showed: the source, the
// commit the plan resolved, and a digest of the values it used. The run
// deploys that commit, so what is approved is what is deployed, or it
// refuses (P6-04).
type appPin struct {
	src    apps.Source
	commit string
	values string
}

// valuesDigest sums up the values a deploy uses, and whether the chart's
// own values.yaml counts: nothing else the record holds changes a render.
func (a *appChart) valuesDigest() string {
	return digest(a.vals, a.rec != nil && a.rec.IgnoreDefaults)
}

// appRefusal says why a person may not use --app in the command's
// namespace, or "": it reads a record with the console's account, so the
// person's rights are asked here, for the role and for that namespace. The
// answer is the same whether a record is kept there or not.
func (s *Service) appRefusal(ctx context.Context, cl Cluster, who Who, c *Command) string {
	if c == nil || !c.Has("app") {
		return ""
	}
	if !who.ManageApps {
		return "Only Admins and SuperAdmins use an application's saved values and token."
	}
	s.mu.Lock()
	ask := s.opts.AppAccess
	s.mu.Unlock()
	if ask == nil {
		return ""
	}
	ns := helmNamespace(c)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	may, err := ask(ctx, cl, who, ns)
	if err != nil {
		return "Asking Kubernetes what you may do in " + ns + " failed: " + err.Error()
	}
	if !may {
		return "Kubernetes does not let you manage applications in " + ns + "."
	}
	return ""
}

// appChart fetches and loads the chart a command names, with the values
// it asks for. The record, when the command uses one, supplies the
// source, the token, the values file and the parameters.
type appChart struct {
	src    apps.Source
	rec    *apps.Record
	co     *apps.Checkout
	vals   map[string]any
	values string
	params []string
	// set are the line's --set parameters.
	set []string
	// composed says the values need the checkout: see loadAppChart.
	composed bool
}

func loadAppChart(ctx context.Context, c *Command, t Target, ns, name string) (*appChart, error) {
	src, given, err := appSource(c)
	if err != nil {
		return nil, err
	}
	// A repository's chart takes its address from the repositories the
	// console knows (HR-07).
	if given && src.FromRepository() {
		if t.Repos == nil {
			return nil, errors.New("this console keeps no chart repositories: name a chart in git with --git")
		}
		if src, err = t.Repos.Resolve(ctx, src); err != nil {
			return nil, err
		}
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
			// The saved token and values belong to the saved repository:
			// another one on the line would receive them.
			if given && !apps.SameRepo(src, rec.Source) {
				return nil, fmt.Errorf("%s/%s is kept with its chart in %s: --app cannot take its token and values to another repository", ns, name, keptIn(rec.Source))
			}
			a.rec, token = &rec, tok
			if !given {
				a.src = rec.Source
			}
			a.values, a.params = rec.Values, rec.Params
		}
	}
	if a.src.Repo == "" {
		return nil, errors.New("name the chart: --git URL with --ref BRANCH|TAG for a chart in git, or REPO/CHART or oci://REGISTRY/PATH/CHART with --version")
	}
	for _, set := range c.Flags["set"] {
		a.params = append(a.params, set)
	}
	if a.vals, err = apps.Values(a.values, a.params); err != nil {
		return nil, err
	}
	pinned := t.pin != nil && t.pin.commit != ""
	// A repository's chart named without --version deploys the version
	// its card settled on (HR-08).
	if pinned && a.src.FromRepository() && a.src.Ref == "" {
		a.src.Ref = t.pin.src.Ref
	}
	if pinned {
		if t.pin.src != a.src {
			return nil, errors.New("the application's source changed after this card was made: send the command again for a fresh card")
		}
		a.co, err = t.Repos.FetchDigest(ctx, a.src, token, t.pin.commit)
		if err != nil {
			return nil, fmt.Errorf("%s %s, which this card shows, cannot be fetched: %w. Send the command again for a fresh card", commitWord(t.pin.commit), short(t.pin.commit), err)
		}
	} else if a.co, err = t.Repos.Fetch(ctx, a.src, token); err != nil {
		return nil, err
	}
	if a.src.FromRepository() {
		a.src.Ref = a.co.Version
	}
	// The record's second values file, its fleet and whether the chart's
	// own values.yaml counts need the checkout (R2-08, R2-09, R2-11).
	a.set = c.Flags["set"]
	a.composed = a.rec != nil && (a.rec.ValuesFile != "" || a.rec.IgnoreDefaults || a.rec.FleetLabel != "")
	if a.composed {
		if _, a.vals, err = apps.Compose(a.co, a.src, a.rec, a.set); err != nil {
			return nil, err
		}
	}
	if pinned && t.pin.values != a.valuesDigest() {
		return nil, errors.New("the application's values changed after this card was made: send the command again for a fresh card")
	}
	return a, nil
}

// pinTo keeps what the card shows, for its dry run and its run.
func (a *appChart) pinTo(pin *appPin) {
	if pin != nil {
		*pin = appPin{src: a.src, commit: a.co.Commit, values: a.valuesDigest()}
	}
}

// load builds the chart from the checkout, a new one each time: Helm
// changes the chart it renders.
func (a *appChart) load() (*chart.Chart, error) {
	if a.composed {
		ch, _, err := apps.Compose(a.co, a.src, a.rec, a.set)
		return ch, err
	}
	ch, err := apps.LoadChart(a.co, a.src.Path)
	if err != nil {
		return nil, err
	}
	if err := apps.CheckDependencies(ch, a.vals); err != nil {
		return nil, err
	}
	return ch, nil
}

// saveDeploy brings the application's record up to date after a deploy.
// The record is read again, since an upgrade can take minutes and a save
// made meanwhile stands: only the source the line named and what was
// deployed change; the first deploy of an application makes its record.
func (a *appChart) saveDeploy(ctx context.Context, c *Command, t Target, out io.Writer, ns, name, chartVersion string) error {
	var err error
	for range 3 {
		if err = a.saveDeployOnce(ctx, c, t, out, ns, name, chartVersion); !errors.Is(err, apps.ErrConflict) {
			return err
		}
	}
	return err
}

func (a *appChart) saveDeployOnce(ctx context.Context, c *Command, t Target, out io.Writer, ns, name, chartVersion string) error {
	now := time.Now().UTC()
	var token *string
	rec, _, err := t.Apps.Get(ctx, t.Cluster, ns, name)
	switch {
	case errors.Is(err, apps.ErrNotFound):
		rec = apps.Record{Cluster: t.Cluster, Namespace: ns, Name: name, Created: now, CreatedBy: t.Person.User}
		if a.rec != nil {
			rec = *a.rec
			rec.Rev = ""
		}
		rec.Source, rec.Values, rec.Params = a.src, a.values, a.params
	case err != nil:
		return err
	case c.Has("git") || len(c.Positionals) > 1:
		// A token belongs to its repository: a new one on the line would
		// receive it at the next deploy.
		if !apps.SameRepo(a.src, rec.Source) {
			empty := ""
			token = &empty
		}
		rec.Source = a.src
	}
	rec.Commit, rec.CommitDate, rec.CommitSubject = a.co.Commit, a.co.CommitDate, a.co.CommitSubject
	rec.ChartVersion, rec.Updated, rec.UpdatedBy = chartVersion, now, t.Person.User
	if err := t.Apps.Save(ctx, rec, token); err != nil {
		return err
	}
	if token != nil {
		fmt.Fprintln(out, "# watson: the application moved to another repository; the token saved for the old one was removed")
	}
	return nil
}

// helmApp runs helm install, upgrade or uninstall.
func helmApp(ctx context.Context, c *Command, t Target, out io.Writer, ns string) error {
	name := c.Positionals[0]
	dry := c.Has("dry-run") && c.Flag("dry-run") != "false" && c.Flag("dry-run") != "none"
	// A dry run prints what it renders, and Watson never prints Secret
	// values. So it renders on the console, where lookup finds nothing,
	// whatever --dry-run says; Secret objects are left out, and values that
	// look like credentials are masked in every kind.
	strategy := action.DryRunNone
	if dry {
		strategy = action.DryRunClient
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

	if !dry && t.pin != nil && t.pin.commit == "" {
		return errors.New("this card could not settle on a commit, so nothing was deployed: send the command again for a fresh card")
	}
	a, err := loadAppChart(ctx, c, t, ns, name)
	if err != nil {
		return err
	}
	ch, err := a.load()
	if err != nil {
		return err
	}
	// A template cannot be stopped once a deploy runs it, so a copy goes
	// through the limits first. Anything else it finds is left to the real
	// render, which has the cluster to ask.
	if check, err := a.load(); err == nil {
		if _, err := apps.Render(ctx, check, a.vals, name, ns, ""); errors.Is(err, apps.ErrRenderLimit) {
			return err
		}
	}
	funcs, _ := apps.RenderFuncs(ch)
	cfg.CustomTemplateFuncs = funcs
	vals := a.vals
	if dry {
		vals = MaskValues(vals)
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
		r, err := i.RunWithContext(ctx, ch, vals)
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
		r, err := u.RunWithContext(ctx, name, ch, vals)
		if err != nil {
			return err
		}
		if rel, err = v1(r); err != nil {
			return err
		}
	}
	if dry {
		fmt.Fprintf(out, "DRY RUN: %s %s from %s at %s %s\n", map[bool]string{true: "install", false: "upgrade"}[install], name, a.src, commitWord(a.co.Commit), short(a.co.Commit))
		fmt.Fprintln(out, "# watson: rendered without reading the cluster: lookup finds nothing here; Secrets are left out and credentials masked")
	}
	if err := (statusPrinter{rel: rel, showMetadata: true}).WriteTable(out); err != nil {
		return err
	}
	if dry {
		fmt.Fprintf(out, "MANIFEST:\n%s\n", strings.TrimSpace(rel.Manifest))
		return nil
	}
	if t.Apps != nil {
		if err := a.saveDeploy(ctx, c, t, out, ns, name, ch.Metadata.Version); err != nil {
			fmt.Fprintf(out, "# watson: the application's record was not saved: %v\n", err)
		}
	}
	return nil
}

func short(commit string) string { return apps.ShortDigest(commit) }

// commitWord is what a card's pin names: a git commit, or a repository
// chart's archive by its digest.
func commitWord(commit string) string {
	if strings.HasPrefix(commit, "sha256:") {
		return "the chart archive"
	}
	return "commit"
}

// keptIn is where an application's chart is kept, for a refusal.
func keptIn(s apps.Source) string {
	if s.FromRepository() {
		return s.ChartRef()
	}
	return s.Repo
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
	rels, relErr := read(ctx, cs)
	if relErr != nil && !apierrors.IsForbidden(relErr) {
		return targets, []guard{unreadable("release "+ns+"/"+name, relErr)}, []string{"Watson could not read the release: " + relErr.Error()}
	}
	if relErr != nil {
		impact = append(impact, couldNotRead("the release", relErr))
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
	if relErr == nil {
		guards = append(guards, guard{what: "release " + ns + "/" + name, want: state(rels), read: func(ctx context.Context, cs *clients) (string, error) {
			rels, err := read(ctx, cs)
			if err != nil {
				return "", err
			}
			return state(rels), nil
		}})
	}

	if c.Name() == "helm uninstall" {
		if relErr != nil {
			return targets, guards, impact
		}
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
		return targets, guards, append(impact, "Watson could not fetch the chart: "+err.Error())
	}
	ch, err := a.load()
	if err != nil {
		return targets, guards, append(impact, "Watson could not load the chart: "+err.Error())
	}
	a.pinTo(t.pin)
	r, err := apps.Render(ctx, ch, a.vals, name, ns, "")
	if err != nil {
		return targets, guards, append(impact, "Watson could not render the chart: "+err.Error())
	}
	when := ""
	if !a.co.CommitDate.IsZero() {
		when = ", " + a.co.CommitDate.Format("Jan 2 2006")
	}
	from := fmt.Sprintf("%s at commit %s (%q%s)", a.src, short(a.co.Commit), a.co.CommitSubject, when)
	if a.src.FromRepository() {
		from = fmt.Sprintf("%s, archive %s%s", a.src, short(a.co.Commit), when)
	}
	install := c.Name() == "helm install" || (c.Has("install") && cur == nil)
	switch {
	case relErr != nil:
		// What runs now is not known, so nothing is said about what changes.
		impact = append(impact, fmt.Sprintf("Deploys chart %s %s (app %s) from %s.", ch.Metadata.Name, ch.Metadata.Version, ch.Metadata.AppVersion, from))
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
	t := Target{Config: cl.Config, Person: who.Person, Cluster: cl.ID, Apps: s.opts.Apps, Repos: s.opts.Repos}
	d, err := s.disc.For(cl.ID, cl.Config)
	if err != nil {
		return "", err
	}
	t.Discovery = d
	cfg, err := helmConfig(ctx, &Command{Namespace: ns}, t, io.Discard, ns)
	if err != nil {
		return "", err
	}
	funcs, tripped := apps.RenderFuncs(ch)
	cfg.CustomTemplateFuncs = funcs
	manifest, err := apps.Bounded(ctx, func(ctx context.Context) (string, error) {
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
		return rel.Manifest, apps.CheckOutput(rel.Manifest)
	})
	if tripped() {
		return "", fmt.Errorf("%w: it loops or repeats too much", apps.ErrRenderLimit)
	}
	return manifest, err
}
