package knowledge

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GitStore keeps the wiki in a git repository: the approved pages on its
// branch, each run's changes on a branch mycroft/<run> until approved,
// when they are merged. A person's commit that came first is met with a
// rebase: Mycroft's change is made again over it, unless both touch the
// same page, which then waits for a person.
type GitStore struct {
	g *repo
	// last is the commit the files were last pulled at, and files them.
	last  plumbing.Hash
	files map[string]File
}

// NewGitStore opens a repository for the wiki, cloned under dir.
func NewGitStore(dir, url, branch, user, token, ca string) *GitStore {
	return &GitStore{g: newRepo(repoDir(dir, "wiki", url, branch), url, branch, user, token, ca)}
}

func (s *GitStore) Kind() string { return StoreGit }

// tries bounds the rebases of one write.
const tries = 6

func draftBranch(run string) string { return "mycroft/" + run }

func (s *GitStore) Pull(ctx context.Context) (map[string]File, error) {
	s.g.mu.Lock()
	defer s.g.mu.Unlock()
	r, head, err := s.g.fetch(ctx)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return map[string]File{}, nil
	}
	if head.Hash == s.last && s.files != nil {
		return copyFiles(s.files), nil
	}
	es, err := entries(head)
	if err != nil {
		return nil, err
	}
	out := map[string]File{}
	for p, e := range es {
		if !wanted(p) {
			continue
		}
		b, err := readBlob(r, e.hash)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		out[p] = File{Path: p, Body: b, Version: e.hash.String()}
	}
	s.last, s.files = head.Hash, out
	return copyFiles(out), nil
}

func copyFiles(in map[string]File) map[string]File {
	out := make(map[string]File, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func bodies(changes []Change) map[string][]byte {
	m := map[string][]byte{}
	for _, c := range changes {
		switch {
		case c.Delete:
			m[c.Path] = nil
		case c.Body == nil:
			m[c.Path] = []byte{}
		default:
			m[c.Path] = c.Body
		}
	}
	return m
}

func (s *GitStore) Write(ctx context.Context, files []Change, msg string) error {
	if len(files) == 0 {
		return nil
	}
	s.g.mu.Lock()
	defer s.g.mu.Unlock()
	for try := 0; try < tries; try++ {
		r, head, err := s.g.fetch(ctx)
		if err != nil {
			return err
		}
		var parents []plumbing.Hash
		if head != nil {
			parents = []plumbing.Hash{head.Hash}
		}
		h, err := s.g.commit(r, head, bodies(files), msg, parents...)
		if err != nil {
			return err
		}
		if err := s.g.push(ctx, r, h, s.g.branch, false); errors.Is(err, errNotFastForward) {
			continue
		} else if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: the branch kept moving; try again", ErrConflictStore)
}

func (s *GitStore) Stage(ctx context.Context, run string, changes []Change, msg string) error {
	s.g.mu.Lock()
	defer s.g.mu.Unlock()
	r, head, err := s.g.fetch(ctx)
	if err != nil {
		return err
	}
	var parents []plumbing.Hash
	if head != nil {
		parents = []plumbing.Hash{head.Hash}
	}
	h, err := s.g.commit(r, head, bodies(changes), msg, parents...)
	if err != nil {
		return err
	}
	return s.g.push(ctx, r, h, draftBranch(run), true)
}

func (s *GitStore) Publish(ctx context.Context, run string, changes []Change, msg string) ([]string, error) {
	s.g.mu.Lock()
	defer s.g.mu.Unlock()
	for try := 0; try < tries; try++ {
		r, head, err := s.g.fetch(ctx)
		if err != nil {
			return nil, err
		}
		draft, err := s.g.commitOf(r, "refs/remotes/origin/"+draftBranch(run))
		if err != nil {
			return nil, fmt.Errorf("%w: run %s has no branch %s in the repository", ErrNotFound, run, draftBranch(run))
		}
		onMain, err := entries(head)
		if err != nil {
			return nil, err
		}
		staged, err := entries(draft)
		if err != nil {
			return nil, err
		}
		var conflicts []string
		apply := map[string][]byte{}
		for _, c := range changes {
			cur, exists := onMain[c.Path]
			if (exists && cur.hash.String() != c.Base) || (!exists && c.Base != "") {
				conflicts = append(conflicts, c.Path)
				continue
			}
			e, ok := staged[c.Path]
			if !ok {
				return nil, fmt.Errorf("%w: %s is not on %s", ErrNotFound, c.Path, draftBranch(run))
			}
			b, err := readBlob(r, e.hash)
			if err != nil {
				return nil, err
			}
			apply[c.Path] = b
		}
		sort.Strings(conflicts)
		if len(apply) == 0 {
			return conflicts, nil
		}
		// A merge of the run's branch, the first time; once merged, the
		// pages approved later follow as ordinary commits.
		var parents []plumbing.Hash
		if head != nil {
			parents = append(parents, head.Hash)
		}
		if merged, _ := isAncestor(draft, head); !merged {
			parents = append(parents, draft.Hash)
		}
		h, err := s.g.commit(r, head, apply, msg, parents...)
		if err != nil {
			return nil, err
		}
		if err := s.g.push(ctx, r, h, s.g.branch, false); errors.Is(err, errNotFastForward) {
			// Someone committed first: rebase, page by page.
			continue
		} else if err != nil {
			return nil, err
		}
		return conflicts, nil
	}
	return nil, fmt.Errorf("%w: the branch kept moving; try again", ErrConflictStore)
}

func isAncestor(c, of *object.Commit) (bool, error) {
	if c == nil || of == nil {
		return false, nil
	}
	if c.Hash == of.Hash {
		return true, nil
	}
	return c.IsAncestor(of)
}

func (s *GitStore) Discard(ctx context.Context, run string) error {
	s.g.mu.Lock()
	defer s.g.mu.Unlock()
	r, err := s.g.open()
	if err != nil {
		return err
	}
	return s.g.deleteBranch(ctx, r, draftBranch(run))
}

func (s *GitStore) Check(ctx context.Context) (StoreCheck, error) {
	files, err := s.Pull(ctx)
	if err != nil {
		return StoreCheck{}, err
	}
	return StoreCheck{Files: len(files)}, nil
}
