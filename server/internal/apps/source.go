// Package apps keeps Helm applications (Phase 10): releases the console
// installed, or adopted, from a chart in a git repository at a branch or
// a tag. The console fetches the chart itself (go-git, in memory: the
// image has no git), renders it with the Helm SDK, and Watson installs,
// upgrades and uninstalls it as the person who asked. What the console
// remembers about an application (its source, the ref, the commit it
// last deployed, its values file and parameters) is a record in a Secret
// of the console's namespace; the release itself is Helm's.
package apps

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// RefKind says what a ref names: a git branch or tag, or a repository
// chart's version.
const (
	RefBranch  = "branch"
	RefTag     = "tag"
	RefVersion = "version"
)

// --- Helm repositories (Phase 12, wave 5, HR-01 to HR-12) ---

// The kinds of source: a git repository (the default, also ""), a Helm
// chart repository over HTTP with its index.yaml, or an OCI registry.
const (
	KindGit  = "git"
	KindHelm = "helm"
	KindOCI  = "oci"
)

// Source is where an application's chart comes from: a git repository
// reachable over HTTPS, a directory in it, and a branch or a tag; or a
// chart of a Helm repository or an OCI registry, at a version (HR-01).
type Source struct {
	// Kind is git (or ""), helm or oci.
	Kind string `json:"kind,omitempty"`
	// Repo is the git repository's HTTPS URL (https://github.com/org/app),
	// the Helm repository's URL (https://charts.example.com/stable), or
	// the OCI registry's address with the charts' path, without the
	// chart's name (oci://registry.example.com/charts).
	Repo string `json:"repo"`
	// Path is the chart's directory in a git repository ("" for the root).
	Path string `json:"path,omitempty"`
	// Chart is a Helm repository's or an OCI registry's chart, by name.
	Chart string `json:"chart,omitempty"`
	// Repository names the repository of Settings › Applications the chart
	// comes from, whose credentials it is fetched with: always for a Helm
	// repository, for an OCI registry when one covers its address.
	Repository string `json:"repository,omitempty"`
	// Ref is the branch or tag deployed, or the chart's version.
	Ref string `json:"ref"`
	// RefKind is branch or tag, or version for a repository's chart.
	RefKind string `json:"refKind"`
}

// FromRepository says the chart comes from a Helm repository or an OCI
// registry rather than git.
func (s Source) FromRepository() bool { return s.Kind == KindHelm || s.Kind == KindOCI }

// ChartRef is a repository chart as a helm line names it: repo/chart, or
// oci://registry/path/chart.
func (s Source) ChartRef() string {
	if s.Kind == KindOCI {
		return s.Repo + "/" + s.Chart
	}
	return s.Repository + "/" + s.Chart
}

// String is the source as the table and the audit log show it:
// github.com/org/app/charts/app @ v1.2.0; bitnami/nginx @ 18.1.0;
// oci://ghcr.io/org/charts/app @ 1.2.0.
func (s Source) String() string {
	if s.FromRepository() {
		v := s.Ref
		if v == "" {
			v = "newest"
		}
		return s.ChartRef() + " @ " + v
	}
	repo := strings.TrimPrefix(strings.TrimPrefix(s.Repo, "https://"), "http://")
	repo = strings.TrimSuffix(repo, ".git")
	if s.Path != "" {
		repo += "/" + s.Path
	}
	return repo + " @ " + s.Ref
}

var refName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]*$`)

// chartPath is a chart's directory: plain path segments that do not start
// with a dash. It goes on a Watson line, so nothing in it may read as a
// space, a line break, a quote or a flag.
var chartPath = regexp.MustCompile(`^[A-Za-z0-9._+@][A-Za-z0-9._+@-]*(/[A-Za-z0-9._+@][A-Za-z0-9._+@-]*)*$`)

// AllowLocalRepos lets a repository be a directory on the server's own
// disk: for tests, which clone a repository they made. Never set by the
// server.
var AllowLocalRepos bool

// Normalize cleans a source as typed and says what is wrong with it: the
// repository must be an HTTPS URL (github.com/org/app is completed) on a
// host the console connects to, the path must stay inside the repository
// and hold plain path characters only, and the ref must be a plain name.
func Normalize(s Source) (Source, error) {
	switch s.Kind {
	case KindHelm, KindOCI:
		return normalizeRepoChart(s)
	case "", KindGit:
		s.Kind = ""
	default:
		return s, fmt.Errorf("a source is git, helm (a chart repository) or oci (an OCI registry), not %q", s.Kind)
	}
	repo := strings.TrimSpace(s.Repo)
	if repo == "" {
		return s, errors.New("name the repository, as https://github.com/org/app")
	}
	if AllowLocalRepos && strings.HasPrefix(repo, "/") {
		s.Repo = path.Clean(repo)
		return normalizeRest(s)
	}
	if !strings.Contains(repo, "://") {
		repo = "https://" + repo
	}
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return s, errors.New("the repository must be an https:// URL without a user, query or fragment")
	}
	if refusedName(u.Hostname()) {
		return s, errHostRefused
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	if u.Path == "" || u.Path == "/" {
		return s, errors.New("the repository URL needs a path: https://github.com/org/app")
	}
	s.Repo = u.String()
	return normalizeRest(s)
}

// normalizeRest checks the path, the ref and its kind.
func normalizeRest(s Source) (Source, error) {
	p := strings.Trim(strings.TrimSpace(s.Path), "/")
	if p != "" {
		p = path.Clean(p)
		if p == "." {
			p = ""
		} else if strings.HasPrefix(p, "../") || p == ".." {
			return s, errors.New("the chart path must be inside the repository")
		} else if !chartPath.MatchString(p) {
			return s, errors.New("the chart path may hold letters, digits and . _ + @ -, in segments that do not start with a dash")
		}
	}
	s.Path = p
	s.Ref = strings.TrimSpace(s.Ref)
	if s.Ref == "" {
		return s, errors.New("name a branch or a tag")
	}
	if !refName.MatchString(s.Ref) || strings.Contains(s.Ref, "..") || strings.HasSuffix(s.Ref, ".lock") || strings.HasSuffix(s.Ref, "/") {
		return s, fmt.Errorf("%q is not a branch or tag name", s.Ref)
	}
	switch s.RefKind {
	case RefBranch, RefTag:
	case "":
		s.RefKind = RefBranch
	default:
		return s, fmt.Errorf("refKind must be %s or %s", RefBranch, RefTag)
	}
	return s, nil
}

// SameRepo says whether two sources name one repository, as Normalize
// writes it. A token belongs to its repository: it never follows a source
// to another one. A source that cannot be normalized matches nothing.
func SameRepo(a, b Source) bool {
	if a.FromRepository() || b.FromRepository() {
		x, err := Normalize(Source{Kind: a.Kind, Repo: a.Repo, Chart: "x", Repository: a.Repository})
		if err != nil {
			return false
		}
		y, err := Normalize(Source{Kind: b.Kind, Repo: b.Repo, Chart: "x", Repository: b.Repository})
		if err != nil || x.Kind != y.Kind {
			return false
		}
		if x.Repo == "" || y.Repo == "" {
			return x.Repository != "" && x.Repository == y.Repository
		}
		return x.Repo == y.Repo
	}
	x, err := Normalize(Source{Repo: a.Repo, Ref: "x"})
	if err != nil {
		return false
	}
	y, err := Normalize(Source{Repo: b.Repo, Ref: "x"})
	return err == nil && x.Repo == y.Repo
}

// chartName is a repository chart's name, as Helm allows them; it goes on
// a Watson line, so nothing in it reads as a flag or a separator.
var chartName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// chartVersion is a chart's version: a semantic version or a plain tag.
var chartVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$`)

// ociPath is an OCI repository's path, as the distribution spec allows.
var ociPath = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$`)

// RepoURL cleans a Helm repository's or an OCI registry's address and
// says what is wrong with it: https:// (or oci://) on a host the console
// connects to, without a user, query or fragment. A Helm repository's
// trailing slash and index.yaml are taken off.
func RepoURL(kind, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch kind {
	case KindHelm:
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || AllowLocalRepos && u.Scheme == "http") {
			return "", errors.New("a Helm repository's address is an https:// URL without a user, query or fragment")
		}
		if refusedName(u.Hostname()) && !AllowLocalRepos {
			return "", errHostRefused
		}
		u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/index.yaml"), "/")
		return u.String(), nil
	case KindOCI:
		rest, ok := strings.CutPrefix(raw, "oci://")
		if !ok {
			return "", errors.New("an OCI registry's address starts with oci://, as oci://registry.example.com/charts")
		}
		host, p, _ := strings.Cut(strings.TrimSuffix(rest, "/"), "/")
		u, err := url.Parse("https://" + host)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || host == "" {
			return "", errors.New("an OCI registry's address is oci://host[:port]/path")
		}
		if refusedName(u.Hostname()) && !AllowLocalRepos {
			return "", errHostRefused
		}
		if p != "" && !ociPath.MatchString(p) {
			return "", errors.New("an OCI registry's path holds lowercase letters, digits and . _ - between slashes")
		}
		out := "oci://" + strings.ToLower(host)
		if p != "" {
			out += "/" + p
		}
		return out, nil
	}
	return "", fmt.Errorf("a repository is helm or oci, not %q", kind)
}

// normalizeRepoChart checks a Helm repository's or an OCI registry's
// chart: the address, the chart's name, and the version, which may be
// left empty for the newest (a deploy settles it before anything is
// kept). A Helm repository's chart is named by the repository it is in;
// the address is the repository's, filled in from the list of
// repositories (Repos.Resolve).
func normalizeRepoChart(s Source) (Source, error) {
	s.Path = ""
	s.Chart = strings.TrimSpace(s.Chart)
	s.Repository = strings.TrimSpace(s.Repository)
	if s.Kind == KindHelm && s.Repository == "" {
		return s, errors.New("name the chart repository, as Settings › Applications lists it")
	}
	if s.Repository != "" && !repoName.MatchString(s.Repository) {
		return s, fmt.Errorf("%q is not a repository's name", s.Repository)
	}
	if s.Repo != "" || s.Kind == KindOCI {
		u, err := RepoURL(s.Kind, s.Repo)
		if err != nil {
			return s, err
		}
		s.Repo = u
	}
	if !chartName.MatchString(s.Chart) {
		return s, errors.New("name the chart: letters, digits and . _ -, starting with a letter or digit")
	}
	s.Ref = strings.TrimSpace(s.Ref)
	if s.Ref != "" && !chartVersion.MatchString(s.Ref) {
		return s, fmt.Errorf("%q is not a chart version", s.Ref)
	}
	s.RefKind = RefVersion
	return s, nil
}

// ParseChartRef reads a chart as a helm line names it (HR-07): repo/chart,
// a chart of a repository Settings › Applications lists, or
// oci://registry/path/chart; version is --version, "" for the newest.
func ParseChartRef(ref, version string) (Source, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "oci://") {
		i := strings.LastIndex(ref, "/")
		if i <= len("oci://") {
			return Source{}, errors.New("an OCI chart is oci://registry/path/chart")
		}
		return Normalize(Source{Kind: KindOCI, Repo: ref[:i], Chart: ref[i+1:], Ref: version})
	}
	repo, chart, ok := strings.Cut(ref, "/")
	if !ok || strings.Contains(chart, "/") || strings.Contains(ref, "://") {
		return Source{}, fmt.Errorf("%q is not a chart: name it as repo/chart, with a repository Settings › Applications lists, or as oci://registry/path/chart (a chart in git is --git URL)", ref)
	}
	return Normalize(Source{Kind: KindHelm, Repository: repo, Chart: chart, Ref: version})
}

// Record is what the console keeps about an application (HA-03).
// Command is a named redeploy of an application with its own parameters
// (asked October 30, 2026): `helm upgrade` from the source with these
// --set parameters applied over the saved values and parameters.
type Command struct {
	Name string `json:"name"`
	// Note says what the command is for, in the menu.
	Note string `json:"note,omitempty"`
	// Params are the extra parameters, each key=value as helm --set takes
	// them.
	Params    []string  `json:"params"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"createdBy,omitempty"`
	Updated   time.Time `json:"updated"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

// MaxCommands is how many commands an application may have.
const MaxCommands = 20

// MaxCommandNote is how long a command's note may be: it is the menu
// item's hint, beside the name, and the menu is 440 pixels wide at most.
const MaxCommandNote = 48

// CommandIndex finds a command by name, case-insensitively; -1 is none.
func (r Record) CommandIndex(name string) int {
	for i, c := range r.Commands {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

// CheckCommandName says why a command name is not allowed: one to forty
// characters of letters, digits, spaces, dots, dashes and underscores,
// starting with a letter or digit.
func CheckCommandName(name string) error {
	if name == "" || len(name) > 40 {
		return errors.New("a command name is one to forty characters")
	}
	for i, ch := range name {
		letter := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
		if i == 0 && !letter {
			return errors.New("a command name starts with a letter or a digit")
		}
		if !letter && ch != ' ' && ch != '.' && ch != '-' && ch != '_' {
			return errors.New("a command name has letters, digits, spaces, dots, dashes and underscores")
		}
	}
	if strings.HasSuffix(name, " ") {
		return errors.New("a command name does not end with a space")
	}
	return nil
}

type Record struct {
	// Version marks the format, for migrations (P7-03).
	Version   int    `json:"version"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Source    Source `json:"source"`
	// Commit is the commit last installed or upgraded from, with when it
	// was made and its first line, as the drawer shows them.
	Commit        string    `json:"commit,omitempty"`
	CommitDate    time.Time `json:"commitDate,omitempty"`
	CommitSubject string    `json:"commitSubject,omitempty"`
	// ChartVersion is the chart's version at that commit.
	ChartVersion string `json:"chartVersion,omitempty"`
	// Values is the application's values file, as YAML (HA-12).
	Values string `json:"values,omitempty"`
	// Params are the extra parameters, each key=value as helm --set takes
	// them, applied over the values file (HA-13).
	Params []string `json:"params,omitempty"`
	// CreateNamespace makes the namespace at install when it is missing.
	CreateNamespace bool `json:"createNamespace,omitempty"`
	// ValuesFile is a second values file in the chart's directory
	// (values-prod.yaml), read at the ref and applied over the chart's
	// values.yaml, under Values and Params (R2-08). "" is none.
	ValuesFile string `json:"valuesFile,omitempty"`
	// IgnoreDefaults leaves the chart's own values.yaml out (R2-09).
	IgnoreDefaults bool `json:"ignoreDefaults,omitempty"`
	// Fleet is the fleet the application is deployed to, by id, and
	// FleetLabel the fleet's deploy label when it was saved: the pods'
	// nodeSelector (R2-11).
	Fleet      string `json:"fleet,omitempty"`
	FleetLabel string `json:"fleetLabel,omitempty"`
	// Commands are the application's own deploy commands (asked October
	// 30, 2026): each redeploys the release from its source with extra
	// --set parameters over the saved values and parameters. Names are
	// unique within the application.
	Commands []Command `json:"commands,omitempty"`
	// MatchedRevision is the deployed revision the source was found to
	// render exactly when it was set (round 2 review): the release is not
	// behind the source while that revision runs.
	MatchedRevision int `json:"matchedRevision,omitempty"`
	// SourceDiff says how the source rendered differently from what runs
	// when it was set ("23 objects at 767275b73ce4"), or why it could
	// not be compared; "" when it matched or was not compared.
	SourceDiff string `json:"sourceDiff,omitempty"`
	// HasToken says a credential for the repository is saved with the
	// record; the token itself never leaves the server.
	HasToken bool `json:"hasToken,omitempty"`
	// Rev is the stored version Get read, which Save holds it to; it is
	// not part of the record.
	Rev string `json:"-"`
	// CreatorNamespaces are the namespaces whoever created it could reach
	// then: someone with the same access sees it, even before the release
	// has a namespace to be seen in.
	CreatorNamespaces []string  `json:"creatorNamespaces,omitempty"`
	CreatedBy         string    `json:"createdBy,omitempty"`
	Created           time.Time `json:"created"`
	UpdatedBy         string    `json:"updatedBy,omitempty"`
	Updated           time.Time `json:"updated"`
}

// RecordVersion is the current format of a saved record.
const RecordVersion = 1

// Key identifies an application on its cluster: namespace/name.
func (r Record) Key() string { return r.Namespace + "/" + r.Name }

// ID is the short, stable id a record is stored under.
func ID(cluster, namespace, name string) string {
	sum := sha256.Sum256([]byte(cluster + "\x00" + namespace + "\x00" + name))
	return hex.EncodeToString(sum[:6])
}

// ParseParams reads parameters, one key=value per line (HA-13); blank
// lines and # comments are skipped.
func ParseParams(text string) ([]string, error) {
	var out []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("line %d: a parameter is key=value", i+1)
		}
		out = append(out, line)
	}
	return out, nil
}
