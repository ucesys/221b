package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/mod/semver"
	"helm.sh/helm/v4/pkg/chart/loader/archive"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/ignore"
)

// Refs is what a repository offers (HA-05): its branches and tags, and the
// branch its HEAD points at.
type Refs struct {
	Default  string   `json:"default,omitempty"`
	Branches []string `json:"branches"`
	Tags     []string `json:"tags"`
	// Heads is each ref's commit, for telling whether a branch moved.
	Heads map[string]string `json:"heads,omitempty"`
}

// fetchTimeout bounds one ls-remote or clone.
const fetchTimeout = 90 * time.Second

func auth(token string) transport.AuthMethod {
	if token == "" {
		return nil
	}
	// GitHub takes a token as the password of any user name; so do GitLab
	// and Gitea.
	return &githttp.BasicAuth{Username: "x-access-token", Password: token}
}

// ListRefs asks the repository for its branches and tags, as git
// ls-remote does, without fetching anything else.
func ListRefs(ctx context.Context, repo, token string) (Refs, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{repo}})
	list, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth(token), PeelingOption: git.IgnorePeeled})
	if err != nil {
		return Refs{}, fmt.Errorf("reading the repository's branches and tags: %w", describe(err))
	}
	out := Refs{Branches: []string{}, Tags: []string{}, Heads: map[string]string{}}
	var head plumbing.Hash
	for _, ref := range list {
		switch {
		case ref.Name() == plumbing.HEAD:
			if ref.Type() == plumbing.SymbolicReference {
				out.Default = ref.Target().Short()
			} else {
				head = ref.Hash()
			}
		case ref.Name().IsBranch():
			out.Branches = append(out.Branches, ref.Name().Short())
			out.Heads[ref.Name().Short()] = ref.Hash().String()
		case ref.Name().IsTag():
			out.Tags = append(out.Tags, ref.Name().Short())
			out.Heads[ref.Name().Short()] = ref.Hash().String()
		}
	}
	if out.Default == "" && !head.IsZero() {
		for _, b := range out.Branches {
			if out.Heads[b] == head.String() {
				out.Default = b
				break
			}
		}
	}
	sort.Strings(out.Branches)
	sortTags(out.Tags)
	return out, nil
}

// sortTags puts the newest version first: semantic versions in order,
// then the other names alphabetically.
func sortTags(tags []string) {
	sort.SliceStable(tags, func(i, j int) bool {
		vi, vj := semverOf(tags[i]), semverOf(tags[j])
		switch {
		case vi != "" && vj != "":
			return semver.Compare(vi, vj) > 0
		case vi != "":
			return true
		case vj != "":
			return false
		}
		return tags[i] < tags[j]
	})
}

// semverOf is the tag as a semantic version, or "".
func semverOf(tag string) string {
	v := tag
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if semver.IsValid(v) {
		return v
	}
	return ""
}

// NewerTags are the tags of the same major version newer than the one
// deployed, newest first (HA-16), or nil when the deployed ref is not a
// version.
func NewerTags(deployed string, tags []string) []string {
	cur := semverOf(deployed)
	if cur == "" {
		return nil
	}
	var out []string
	for _, t := range tags {
		v := semverOf(t)
		if v != "" && semver.Compare(v, cur) > 0 && semver.Major(v) == semver.Major(cur) {
			out = append(out, t)
		}
	}
	sortTags(out)
	return out
}

// Checkout is a repository fetched at one ref: the commit and the files.
type Checkout struct {
	Commit        string
	CommitDate    time.Time
	CommitSubject string
	FS            billy.Filesystem
}

// Fetch clones the repository at the ref, shallow and in memory (HA-04).
func Fetch(ctx context.Context, s Source, token string) (*Checkout, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	ref := plumbing.NewBranchReferenceName(s.Ref)
	if s.RefKind == RefTag {
		ref = plumbing.NewTagReferenceName(s.Ref)
	}
	fsys := memfs.New()
	r, err := git.CloneContext(ctx, memory.NewStorage(), fsys, &git.CloneOptions{
		URL: s.Repo, Auth: auth(token), ReferenceName: ref, SingleBranch: true, Depth: 1, Tags: git.NoTags,
	})
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) || strings.Contains(err.Error(), "couldn't find remote ref") {
			return nil, fmt.Errorf("the repository has no %s %s", s.RefKind, s.Ref)
		}
		return nil, fmt.Errorf("fetching %s at %s %s: %w", s.Repo, s.RefKind, s.Ref, describe(err))
	}
	head, err := r.Head()
	if err != nil {
		return nil, fmt.Errorf("reading the fetched commit: %w", err)
	}
	out := &Checkout{Commit: head.Hash().String(), FS: fsys}
	if c, err := r.CommitObject(head.Hash()); err == nil {
		out.CommitDate = c.Author.When.UTC()
		out.CommitSubject, _, _ = strings.Cut(strings.TrimSpace(c.Message), "\n")
	}
	return out, nil
}

// describe makes go-git's errors readable: a wrong credential, a
// repository that is not there, a host that does not answer.
func describe(err error) error {
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return errors.New("the repository needs a token, or the token is wrong")
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return errors.New("no repository at that URL (a private one needs a token)")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the repository did not answer in time")
	}
	return err
}

// LoadChart reads the chart in the checkout's directory (HA-04), keeping
// its .helmignore, and checks that its dependencies are vendored: the
// console fetches nothing from chart repositories.
func LoadChart(c *Checkout, dir string) (*chart.Chart, error) {
	root := "/"
	if dir != "" {
		root = "/" + dir
	}
	if fi, err := c.FS.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("the repository has no directory %q at this ref", dir)
	}
	if _, err := c.FS.Stat(path.Join(root, "Chart.yaml")); err != nil {
		return nil, fmt.Errorf("%q has no Chart.yaml: name the chart's directory", dir)
	}
	rules := ignore.Empty()
	if f, err := c.FS.Open(path.Join(root, ignore.HelmIgnore)); err == nil {
		if r, err := ignore.Parse(f); err == nil {
			rules = r
		}
		f.Close()
	}
	rules.AddDefaults()
	var files []*archive.BufferedFile
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := c.FS.ReadDir(path.Join(root, rel))
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := path.Join(rel, e.Name())
			if rules.Ignore(name, e) {
				continue
			}
			if e.IsDir() {
				if err := walk(name); err != nil {
					return err
				}
				continue
			}
			if e.Mode()&fs.ModeSymlink != 0 {
				continue
			}
			f, err := c.FS.Open(path.Join(root, name))
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(f, maxChartFile+1))
			f.Close()
			if err != nil {
				return err
			}
			if len(data) > maxChartFile {
				return fmt.Errorf("%s is larger than %d MB", name, maxChartFile>>20)
			}
			files = append(files, &archive.BufferedFile{Name: name, Data: data})
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, fmt.Errorf("reading the chart's files: %w", err)
	}
	ch, err := loader.LoadFiles(files)
	if err != nil {
		return nil, fmt.Errorf("loading the chart: %w", err)
	}
	var missing []string
	for _, d := range ch.Metadata.Dependencies {
		found := false
		for _, sub := range ch.Dependencies() {
			if sub.Name() == d.Name || (d.Alias != "" && sub.Name() == d.Alias) {
				found = true
			}
		}
		if !found && (d.Condition == "" || d.Enabled) {
			missing = append(missing, d.Name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the chart depends on %s, which the repository does not vendor under charts/: the console fetches no chart repositories", strings.Join(missing, ", "))
	}
	return ch, nil
}

// maxChartFile bounds one file of a chart.
const maxChartFile = 4 << 20
