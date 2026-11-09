package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// repo is a git repository the console reads and writes by HTTPS (or a
// local path, in tests): a bare clone on the pod's disk, fetched again on
// each read. Commits are made without a working tree, from the files of
// the commit they follow, so nothing but the objects is kept on disk.
type repo struct {
	url, branch string
	user, token string
	ca          []byte
	dir         string
	mu          sync.Mutex
	r           *git.Repository
}

// maxFile bounds a file read from a repository.
const maxFile = 8 << 20

func newRepo(dir, url, branch, user, token, ca string) *repo {
	if branch == "" {
		branch = "main"
	}
	return &repo{url: url, branch: branch, user: user, token: token, ca: []byte(ca), dir: dir}
}

func (g *repo) auth() transport.AuthMethod {
	if g.token == "" {
		return nil
	}
	user := g.user
	if user == "" {
		// GitHub takes a token as the password of any user name; so do
		// GitLab and Gitea.
		user = "x-access-token"
	}
	return &githttp.BasicAuth{Username: user, Password: g.token}
}

func (g *repo) caBundle() []byte {
	if len(strings.TrimSpace(string(g.ca))) == 0 {
		return nil
	}
	return g.ca
}

// open opens the bare clone, making it the first time.
func (g *repo) open() (*git.Repository, error) {
	if g.r != nil {
		return g.r, nil
	}
	r, err := git.PlainOpen(g.dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if err := os.MkdirAll(g.dir, 0o700); err != nil {
			return nil, err
		}
		if r, err = git.PlainInit(g.dir, true); err != nil {
			return nil, err
		}
		if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{g.url}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	g.r = r
	return r, nil
}

// describe turns go-git's errors into what a person can act on.
func describeGit(err error) error {
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return errors.New("the repository refused the token: check it can read (and, for the wiki, write) the repository")
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return errors.New("there is no such repository, or the token cannot see it")
	}
	return err
}

// fetch brings every branch up to date and returns the commit the branch
// is at (nil for a repository with no commits yet).
func (g *repo) fetch(ctx context.Context) (*git.Repository, *object.Commit, error) {
	r, err := g.open()
	if err != nil {
		return nil, nil, err
	}
	err = r.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"}, Auth: g.auth(), CABundle: g.caBundle(), Force: true})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		return r, nil, nil
	default:
		return nil, nil, fmt.Errorf("fetching %s: %w", g.url, describeGit(err))
	}
	c, err := g.commitOf(r, "refs/remotes/origin/"+g.branch)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return r, nil, nil
	}
	return r, c, err
}

func (g *repo) commitOf(r *git.Repository, ref string) (*object.Commit, error) {
	h, err := r.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		return nil, err
	}
	return r.CommitObject(h.Hash())
}

// entry is one file of a tree: its blob and mode.
type entry struct {
	hash plumbing.Hash
	mode filemode.FileMode
}

// entries are every file of a commit's tree, submodules too; none for no
// commit.
func entries(c *object.Commit) (map[string]entry, error) {
	out := map[string]entry{}
	if c == nil {
		return out, nil
	}
	t, err := c.Tree()
	if err != nil {
		return nil, err
	}
	w := object.NewTreeWalker(t, true, nil)
	defer w.Close()
	for {
		name, e, err := w.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if e.Mode == filemode.Dir {
			continue
		}
		out[name] = entry{hash: e.Hash, mode: e.Mode}
	}
}

// readBlob reads one file, maxFile at most.
func readBlob(r *git.Repository, h plumbing.Hash) ([]byte, error) {
	b, err := r.BlobObject(h)
	if err != nil {
		return nil, err
	}
	if b.Size > maxFile {
		return nil, fmt.Errorf("the file is over %d MiB", maxFile>>20)
	}
	rd, err := b.Reader()
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	return io.ReadAll(io.LimitReader(rd, maxFile))
}

func putBlob(r *git.Repository, b []byte) (plumbing.Hash, error) {
	obj := r.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(b)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(b); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.Storer.SetEncodedObject(obj)
}

// writeTree writes the trees of a set of files, bottom up, and returns
// the root's.
func writeTree(r *git.Repository, files map[string]entry) (plumbing.Hash, error) {
	subs := map[string]map[string]entry{}
	var list []object.TreeEntry
	for p, e := range files {
		if dir, rest, ok := strings.Cut(p, "/"); ok {
			if subs[dir] == nil {
				subs[dir] = map[string]entry{}
			}
			subs[dir][rest] = e
			continue
		}
		list = append(list, object.TreeEntry{Name: p, Mode: e.mode, Hash: e.hash})
	}
	for dir, m := range subs {
		h, err := writeTree(r, m)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		list = append(list, object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: h})
	}
	// Git sorts a tree's entries by name, a directory's as if it ended in /.
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(list, func(i, j int) bool { return key(list[i]) < key(list[j]) })
	obj := r.Storer.NewEncodedObject()
	if err := (&object.Tree{Entries: list}).Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.Storer.SetEncodedObject(obj)
}

var signature = object.Signature{Name: "Mycroft (221B)", Email: "mycroft@221b.invalid"}

// commit writes a commit of the files over parents' first, with changes
// applied, and returns it.
func (g *repo) commit(r *git.Repository, base *object.Commit, changes map[string][]byte, msg string, parents ...plumbing.Hash) (plumbing.Hash, error) {
	files, err := entries(base)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	for p, body := range changes {
		if body == nil {
			// A file deleted (Change.Delete).
			delete(files, p)
			continue
		}
		h, err := putBlob(r, body)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		files[p] = entry{hash: h, mode: filemode.Regular}
	}
	tree, err := writeTree(r, files)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sig := signature
	sig.When = time.Now()
	c := &object.Commit{Author: sig, Committer: sig, Message: msg, TreeHash: tree, ParentHashes: parents}
	obj := r.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.Storer.SetEncodedObject(obj)
}

// push sets a branch of the repository to a commit; force moves it
// whatever it holds. A push that is not a fast-forward returns
// errNotFastForward, for the caller to fetch again and redo.
func (g *repo) push(ctx context.Context, r *git.Repository, h plumbing.Hash, branch string, force bool) error {
	local := plumbing.ReferenceName("refs/heads/221b-out")
	if err := r.Storer.SetReference(plumbing.NewHashReference(local, h)); err != nil {
		return err
	}
	spec := string(local) + ":refs/heads/" + branch
	if force {
		spec = "+" + spec
	}
	err := r.PushContext(ctx, &git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(spec)}, Auth: g.auth(), CABundle: g.caBundle()})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
		return nil
	case errors.Is(err, git.ErrNonFastForwardUpdate), strings.Contains(err.Error(), "non-fast-forward"), strings.Contains(err.Error(), "fetch first"):
		return errNotFastForward
	}
	return fmt.Errorf("pushing to %s: %w", g.url, describeGit(err))
}

var errNotFastForward = errors.New("the branch moved on")

// deleteBranch removes a branch of the repository; one not there is no
// error.
func (g *repo) deleteBranch(ctx context.Context, r *git.Repository, branch string) error {
	err := r.PushContext(ctx, &git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(":refs/heads/" + branch)}, Auth: g.auth(), CABundle: g.caBundle()})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		_ = r.Storer.RemoveReference(plumbing.ReferenceName("refs/remotes/origin/" + branch))
		return nil
	}
	return fmt.Errorf("removing %s: %w", branch, describeGit(err))
}

// repoDir is where a repository's clone is kept under dir: one directory
// per address and branch, so a changed setting starts afresh.
func repoDir(dir, kind, url, branch string) string {
	return filepath.Join(dir, kind+"-"+hashOf([]byte(url + "\x00" + branch))[:16])
}

// hashFrom reads a blob's hash as an item's version holds it.
func hashFrom(s string) (plumbing.Hash, error) {
	if len(s) != 40 || strings.Trim(s, "0123456789abcdef") != "" {
		return plumbing.ZeroHash, fmt.Errorf("%q is not a git object's hash", s)
	}
	return plumbing.NewHash(s), nil
}
