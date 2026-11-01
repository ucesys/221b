package apps

import (
	"bytes"
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
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/mod/semver"
	"helm.sh/helm/v4/pkg/chart/loader/archive"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
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

// Limits bounds what one fetch may hold in memory, so a repository cannot
// take the console down with it. A repository past them is refused
// before its files are written out.
type Limits struct {
	// Pack is the most the repository may send, as it is sent.
	Pack int64
	// Objects is the most objects it may send.
	Objects int64
	// Inflated is the most those objects may come to, unpacked.
	Inflated int64
	// Checkout is the most the files at the ref may come to, and Files how
	// many they and their directories may be.
	Checkout int64
	Files    int64
}

// FetchLimits are the limits of every fetch; tests lower them.
var FetchLimits = Limits{Pack: 64 << 20, Objects: 200_000, Inflated: 128 << 20, Checkout: 128 << 20, Files: 20_000}

// ErrFetchLimit says a repository is larger than a fetch may be.
var ErrFetchLimit = errors.New("the repository is too large for the console to fetch")

// fetchSlots is how many fetches run at once: each may hold what
// FetchLimits allows.
var fetchSlots = make(chan struct{}, 2)

// fetchSlot waits for a turn to fetch.
func fetchSlot(ctx context.Context) (release func(), err error) {
	select {
	case fetchSlots <- struct{}{}:
		return func() { <-fetchSlots }, nil
	case <-ctx.Done():
		return nil, errors.New("the console is fetching other repositories: try again in a moment")
	}
}

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

// boundedStorage keeps objects in memory like memory.Storage, but takes
// the pack whole and checks it against FetchLimits before any object is
// unpacked.
type boundedStorage struct{ *memory.Storage }

func (s boundedStorage) PackfileWriter() (io.WriteCloser, error) {
	return &packBuffer{st: s.Storage}, nil
}

type packBuffer struct {
	st  *memory.Storage
	buf bytes.Buffer
	err error
}

func (p *packBuffer) Write(b []byte) (int, error) {
	if p.err == nil && int64(p.buf.Len()+len(b)) > FetchLimits.Pack {
		p.err = fmt.Errorf("%w: it sends more than %d MB", ErrFetchLimit, FetchLimits.Pack>>20)
	}
	if p.err != nil {
		return 0, p.err
	}
	return p.buf.Write(b)
}

func (p *packBuffer) Close() error {
	if p.err != nil || p.buf.Len() == 0 {
		return p.err
	}
	if err := checkPack(p.buf.Bytes()); err != nil {
		return err
	}
	parser, err := packfile.NewParserWithStorage(packfile.NewScanner(bytes.NewReader(p.buf.Bytes())), p.st)
	if err != nil {
		return err
	}
	_, err = parser.Parse()
	return err
}

// deltaHead keeps the start of a delta, which says how large its result is.
type deltaHead struct{ b []byte }

func (d *deltaHead) Write(p []byte) (int, error) {
	if n := 20 - len(d.b); n > 0 {
		d.b = append(d.b, p[:min(n, len(p))]...)
	}
	return len(p), nil
}

// target reads the delta's result size: the second of its two sizes, each
// seven bits to a byte, lowest first.
func (d *deltaHead) target() int64 {
	var size int64
	var shift uint
	fields := 0
	for _, c := range d.b {
		size |= int64(c&0x7f) << shift
		shift += 7
		if c&0x80 != 0 {
			continue
		}
		if fields++; fields == 2 {
			return size
		}
		size, shift = 0, 0
	}
	return 0
}

// checkPack refuses a pack with too many objects, or objects that would
// unpack to more than FetchLimits allows, counting what each delta builds.
func checkPack(pack []byte) error {
	sc := packfile.NewScanner(bytes.NewReader(pack))
	_, count, err := sc.Header()
	if err != nil {
		return err
	}
	if int64(count) > FetchLimits.Objects {
		return fmt.Errorf("%w: it sends more than %d objects", ErrFetchLimit, FetchLimits.Objects)
	}
	var total int64
	for range count {
		h, err := sc.NextObjectHeader()
		if err != nil {
			return err
		}
		if total += h.Length; total > FetchLimits.Inflated {
			break
		}
		var head deltaHead
		if _, _, err := sc.NextObject(&head); err != nil {
			return err
		}
		if h.Type.IsDelta() {
			total += head.target()
		}
		if total > FetchLimits.Inflated || total < 0 {
			break
		}
	}
	if total > FetchLimits.Inflated || total < 0 {
		return fmt.Errorf("%w: it unpacks to more than %d MB", ErrFetchLimit, FetchLimits.Inflated>>20)
	}
	return nil
}

// checkTree refuses a commit whose files are more or larger than
// FetchLimits allows, reading the sizes from the objects, before any file
// is written out.
func checkTree(st *memory.Storage, tree *object.Tree) error {
	walk := object.NewTreeWalker(tree, true, nil)
	defer walk.Close()
	var files, size int64
	for {
		_, e, err := walk.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if files++; files > FetchLimits.Files {
			return fmt.Errorf("%w: it has more than %d files at this ref", ErrFetchLimit, FetchLimits.Files)
		}
		if e.Mode == filemode.Dir || e.Mode == filemode.Submodule {
			continue
		}
		n, err := st.EncodedObjectSize(e.Hash)
		if err != nil {
			return err
		}
		if size += n; size > FetchLimits.Checkout {
			return fmt.Errorf("%w: its files come to more than %d MB at this ref", ErrFetchLimit, FetchLimits.Checkout>>20)
		}
	}
}

// checkOut writes the commit's files out, once they are within the limits.
func checkOut(r *git.Repository, st boundedStorage, fsys billy.Filesystem, hash plumbing.Hash) (*Checkout, error) {
	c, err := r.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("reading the fetched commit: %w", err)
	}
	tree, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("reading the fetched commit: %w", err)
	}
	if err := checkTree(st.Storage, tree); err != nil {
		return nil, err
	}
	w, err := r.Worktree()
	if err != nil {
		return nil, fmt.Errorf("reading the fetched commit: %w", err)
	}
	if err := w.Checkout(&git.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		return nil, fmt.Errorf("writing out the fetched commit: %w", err)
	}
	out := &Checkout{Commit: hash.String(), FS: fsys, CommitDate: c.Author.When.UTC()}
	out.CommitSubject, _, _ = strings.Cut(strings.TrimSpace(c.Message), "\n")
	return out, nil
}

// Fetch clones the repository at the ref, shallow and in memory (HA-04),
// within FetchLimits.
func Fetch(ctx context.Context, s Source, token string) (*Checkout, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	release, err := fetchSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ref := plumbing.NewBranchReferenceName(s.Ref)
	if s.RefKind == RefTag {
		ref = plumbing.NewTagReferenceName(s.Ref)
	}
	st, fsys := boundedStorage{memory.NewStorage()}, memfs.New()
	r, err := git.CloneContext(ctx, st, fsys, &git.CloneOptions{
		URL: s.Repo, Auth: auth(token), ReferenceName: ref, SingleBranch: true, Depth: 1, Tags: git.NoTags, NoCheckout: true,
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
	return checkOut(r, st, fsys, head.Hash())
}

// FetchCommit fetches one commit of the source, for a deploy that must
// run what its card showed. A server that does not hand out commits by
// their ids can still serve it while the ref points there; anything else
// is refused, so the commit deployed is the one asked for or none.
func FetchCommit(ctx context.Context, s Source, token, commit string) (*Checkout, error) {
	co, err := fetchExact(ctx, s, token, commit)
	if !errors.Is(err, git.ErrExactSHA1NotSupported) {
		return co, err
	}
	if co, err = Fetch(ctx, s, token); err != nil {
		return nil, err
	}
	if co.Commit != commit {
		return nil, fmt.Errorf("the %s %s moved after the card was made, and the repository does not serve commit %s by its id", s.RefKind, s.Ref, commit)
	}
	return co, nil
}

func fetchExact(ctx context.Context, s Source, token, commit string) (*Checkout, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	release, err := fetchSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	st, fsys := boundedStorage{memory.NewStorage()}, memfs.New()
	r, err := git.Init(st, fsys)
	if err != nil {
		return nil, err
	}
	if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{s.Repo}}); err != nil {
		return nil, err
	}
	err = r.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin", Auth: auth(token), Depth: 1, Tags: git.NoTags,
		RefSpecs: []config.RefSpec{config.RefSpec(commit + ":refs/pinned/commit")},
	})
	if errors.Is(err, git.ErrExactSHA1NotSupported) {
		return nil, err
	}
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("fetching commit %s of %s: %w", commit, s.Repo, describe(err))
	}
	return checkOut(r, st, fsys, plumbing.NewHash(commit))
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
	case errors.Is(err, errHostRefused):
		return errHostRefused
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
		if dir == "" {
			return nil, errors.New("the repository's root has no Chart.yaml: give the chart's directory as the path, such as charts/app or deploy/helm/app")
		}
		return nil, fmt.Errorf("%q has no Chart.yaml: give the chart's directory as the path", dir)
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
	return ch, nil
}

// CheckDependencies refuses a chart that needs a dependency it does not
// carry under charts/ (HA-04). Whether it needs one depends on the values
// it deploys with, so Helm's own processing says: it decides the tags and
// conditions, and leaves the dependencies that stay enabled. It works on a
// chart of the missing ones alone, as it changes what it is given and
// Helm processes the chart itself when it renders.
func CheckDependencies(ch *chart.Chart, vals map[string]any) error {
	md := *ch.Metadata
	md.Dependencies = nil
	for _, d := range ch.Metadata.Dependencies {
		found := false
		for _, sub := range ch.Dependencies() {
			if sub.Name() == d.Name || (d.Alias != "" && sub.Name() == d.Alias) {
				found = true
			}
		}
		if !found {
			c := *d
			c.ImportValues = nil
			md.Dependencies = append(md.Dependencies, &c)
		}
	}
	if len(md.Dependencies) == 0 {
		return nil
	}
	probe := &chart.Chart{Metadata: &md, Values: ch.Values}
	if err := chartutil.ProcessDependencies(probe, vals); err != nil {
		return fmt.Errorf("reading the chart's dependencies: %w", err)
	}
	var missing []string
	for _, d := range probe.Metadata.Dependencies {
		missing = append(missing, d.Name)
	}
	if len(missing) > 0 {
		return fmt.Errorf("the chart depends on %s, which the repository does not vendor under charts/: the console fetches no chart repositories", strings.Join(missing, ", "))
	}
	return nil
}

// maxChartFile bounds one file of a chart.
const maxChartFile = 4 << 20
