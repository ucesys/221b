package automation

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GroupsKey holds script groups, so a group exists before its first script.
const GroupsKey = "groups.json"

const (
	MaxGroupDesc = 400
	maxGroups    = 200
)

// ErrInUse is a group that still has scripts.
var ErrInUse = fmt.Errorf("the group still has scripts")

// Group is a named set of scripts. A script names its group; a name only
// scripts use is a group too, so nothing already saved loses its group.
type Group struct {
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
}

func cleanGroup(g *Group) error {
	g.Name = strings.TrimSpace(g.Name)
	switch {
	case g.Name == "":
		return fmt.Errorf("%w: a group has a name", ErrInvalid)
	case len(g.Name) > MaxGroup:
		return fmt.Errorf("%w: a group's name is at most %d characters", ErrInvalid, MaxGroup)
	case len(g.Desc) > MaxGroupDesc:
		return fmt.Errorf("%w: a group's description is at most %d characters", ErrInvalid, MaxGroupDesc)
	}
	return nil
}

// keepGroup records a group a script named, so it outlives its last script
// and stays until someone deletes it.
func (st *Store) keepGroup(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	return change(ctx, st, GroupsKey, 64<<10, func(rec *[]Group) error {
		for _, g := range *rec {
			if g.Name == name {
				return nil
			}
		}
		*rec = append(*rec, Group{Name: name})
		return nil
	})
}

// Groups lists every group, sorted: the ones kept and any a script names.
func (st *Store) Groups(ctx context.Context) ([]Group, error) {
	var kept []Group
	if _, err := st.load(ctx, GroupsKey, &kept); err != nil {
		return nil, err
	}
	scripts, err := st.Scripts(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []Group{}
	for _, g := range kept {
		seen[g.Name] = true
		out = append(out, g)
	}
	for _, s := range scripts {
		if s.Group != "" && !seen[s.Group] {
			seen[s.Group] = true
			out = append(out, Group{Name: s.Group})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SaveGroup adds a group, or with old set changes that one; a new name
// moves its scripts with it.
func (st *Store) SaveGroup(ctx context.Context, old string, g Group) error {
	if err := cleanGroup(&g); err != nil {
		return err
	}
	all, err := st.Groups(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, x := range all {
		if x.Name == g.Name && x.Name != old {
			return fmt.Errorf("%w: a group named %s", ErrExists, g.Name)
		}
		found = found || x.Name == old
	}
	if old != "" && !found {
		return fmt.Errorf("%w: no group %s", ErrNotFound, old)
	}
	if old != "" && old != g.Name {
		err := change(ctx, st, ScriptsKey, maxScriptsBytes, func(rec *scriptsRecord) error {
			for name, s := range rec.Scripts {
				if s.Group == old {
					s.Group = g.Name
					rec.Scripts[name] = s
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return change(ctx, st, GroupsKey, 64<<10, func(rec *[]Group) error {
		kept := (*rec)[:0:0]
		for _, x := range *rec {
			if x.Name != old && x.Name != g.Name {
				kept = append(kept, x)
			}
		}
		if len(kept) >= maxGroups {
			return fmt.Errorf("%w: at most %d groups", ErrFull, maxGroups)
		}
		*rec = append(kept, g)
		return nil
	})
}

// DeleteGroup removes a group with no scripts. Scripts move first, so
// nothing is left in a group that no longer exists.
func (st *Store) DeleteGroup(ctx context.Context, name string) error {
	scripts, err := st.Scripts(ctx)
	if err != nil {
		return err
	}
	var in []string
	for _, s := range scripts {
		if s.Group == name {
			in = append(in, s.Name)
		}
	}
	if len(in) > 0 {
		sort.Strings(in)
		return fmt.Errorf("%w: move %s out first", ErrInUse, strings.Join(in, ", "))
	}
	return change(ctx, st, GroupsKey, 64<<10, func(rec *[]Group) error {
		for i := range *rec {
			if (*rec)[i].Name == name {
				*rec = append((*rec)[:i], (*rec)[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}
