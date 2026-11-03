package automation

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ProfilesKey holds the execution configurations (Auto-1, second step).
const ProfilesKey = "configs.json"

// Kinds of configuration: an SSH one reaches nodes, a Kubernetes one names
// the identity of commands on a cluster.
const (
	ProfileSSH        = "ssh"
	ProfileKubernetes = "kubernetes"
	maxProfiles       = 50
	maxProfilesBytes  = 32 << 10
	// The built-in configurations, which cannot be changed.
	ProfileNobody    = "read-only"
	ProfileSSHPaired = "login-user"
	ProfileLoginUser = "as-the-person"
)

var (
	profileIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	dnsNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

// RunProfile supplies a script's privilege (Configurations page): over
// SSH for nodes, or as a service account on a cluster. Fleets says which
// fleets it reaches.
type RunProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Desc string `json:"desc,omitempty"`
	// Privilege is what an SSH configuration gives: none or full.
	Privilege string `json:"privilege,omitempty"`
	// Namespace and ServiceAccount are a Kubernetes configuration's
	// identity; empty is the person who starts the run.
	Namespace      string `json:"namespace,omitempty"`
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// Fleets are the fleets it reaches; none is every fleet.
	Fleets []string `json:"fleets,omitempty"`
	// Builtin says it comes with the console.
	Builtin bool `json:"builtin,omitempty"`
}

// Builtin are the run profiles every console has. Their ids stay as they
// were, so runs that name one still find it; only their names changed. SSH
// reaches a node only as the account it was paired with.
func builtinProfiles() []RunProfile {
	return []RunProfile{
		{ID: ProfileNobody, Name: "nobody", Kind: ProfileSSH, Privilege: PrivilegeNobody, Builtin: true, Desc: "Read-only: runs as nobody on the node, so it can change nothing. Every dry run uses it."},
		{ID: ProfileSSHPaired, Name: "ssh-paired account", Kind: ProfileSSH, Privilege: PrivilegeElevated, Builtin: true, Desc: "Fully inherited: runs with all the rights of the account each node was paired with, including sudo, up to the fleet's ceiling."},
		{ID: ProfileLoginUser, Name: "login-user", Kind: ProfileKubernetes, Builtin: true, Desc: "Kubernetes commands run as the signed-in person who starts the run, so their own access decides."},
	}
}

// Validate checks a configuration before it is kept.
func (c *RunProfile) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	switch {
	case !profileIDRE.MatchString(c.ID):
		return fmt.Errorf("%w: a run profile's id is lower-case letters, digits and dashes", ErrInvalid)
	case c.ID == ProfileNobody || c.ID == ProfileSSHPaired || c.ID == ProfileLoginUser:
		return fmt.Errorf("%w: %s comes with the console", ErrInvalid, c.ID)
	case c.Name == "" || len(c.Name) > 80:
		return fmt.Errorf("%w: a run profile's name is 1 to 80 characters", ErrInvalid)
	case len(c.Desc) > 400:
		return fmt.Errorf("%w: a description is at most 400 characters", ErrInvalid)
	}
	switch c.Kind {
	case ProfileSSH:
		if c.Privilege != PrivilegeNobody && c.Privilege != PrivilegeElevated {
			return fmt.Errorf("%w: an SSH run profile's privilege is nobody or elevated", ErrInvalid)
		}
		c.Namespace, c.ServiceAccount = "", ""
	case ProfileKubernetes:
		c.Privilege = ""
		if !dnsNameRE.MatchString(c.Namespace) || !dnsNameRE.MatchString(c.ServiceAccount) {
			return fmt.Errorf("%w: a Kubernetes run profile names a namespace and a service account", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: a run profile is ssh or kubernetes", ErrInvalid)
	}
	sort.Strings(c.Fleets)
	return nil
}

// Identity is the Kubernetes identity it names, namespace/name, or "".
func (c RunProfile) Identity() string {
	if c.ServiceAccount == "" {
		return ""
	}
	return c.Namespace + "/" + c.ServiceAccount
}

// Reaches says whether it may be used on a fleet.
func (c RunProfile) Reaches(fleet string) bool {
	if len(c.Fleets) == 0 {
		return true
	}
	for _, f := range c.Fleets {
		if f == fleet {
			return true
		}
	}
	return false
}

// Configs lists the configurations, the built-in ones first.
func (st *Store) Profiles(ctx context.Context) ([]RunProfile, error) {
	var rec []RunProfile
	if _, err := st.load(ctx, ProfilesKey, &rec); err != nil {
		return nil, err
	}
	sort.Slice(rec, func(i, j int) bool { return rec[i].ID < rec[j].ID })
	return append(builtinProfiles(), rec...), nil
}

// Config finds one.
func (st *Store) Profile(ctx context.Context, id string) (RunProfile, bool, error) {
	list, err := st.Profiles(ctx)
	if err != nil {
		return RunProfile{}, false, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, true, nil
		}
	}
	return RunProfile{}, false, nil
}

// SetProfile adds or replaces a configuration.
func (st *Store) SetProfile(ctx context.Context, c RunProfile) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.Builtin = false
	return change(ctx, st, ProfilesKey, maxProfilesBytes, func(rec *[]RunProfile) error {
		for i := range *rec {
			if (*rec)[i].ID == c.ID {
				(*rec)[i] = c
				return nil
			}
		}
		if len(*rec) >= maxProfiles {
			return fmt.Errorf("%w: at most %d run profiles", ErrFull, maxProfiles)
		}
		*rec = append(*rec, c)
		return nil
	})
}

// DeleteProfile removes a configuration; runs that used it keep its name.
func (st *Store) DeleteProfile(ctx context.Context, id string) error {
	return change(ctx, st, ProfilesKey, maxProfilesBytes, func(rec *[]RunProfile) error {
		for i := range *rec {
			if (*rec)[i].ID == id {
				*rec = append((*rec)[:i], (*rec)[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

// ForRun picks and checks the configuration of a run: the one asked for,
// or the built-in that fits. A dry run always uses read-only. A run is
// refused when the configuration gives less than the script declares, or
// more than the fleet allows.
func ForRun(list []RunProfile, id string, h Header, dryRun bool, fleet string, fp FleetVariables) (RunProfile, error) {
	find := func(id string) (RunProfile, bool) {
		for _, c := range list {
			if c.ID == id {
				return c, true
			}
		}
		return RunProfile{}, false
	}
	if h.Target == TargetKubernetes {
		if id == "" {
			id = ProfileLoginUser
		}
		c, ok := find(id)
		if !ok {
			return c, fmt.Errorf("%w: no run profile %q", ErrNotFound, id)
		}
		if c.Kind != ProfileKubernetes {
			return c, fmt.Errorf("%w: %s reaches nodes, and this script targets the cluster", ErrInvalid, c.Name)
		}
		return c, nil
	}
	if dryRun {
		c, _ := find(ProfileNobody)
		return c, nil
	}
	if id == "" {
		id = ProfileSSHPaired
		if h.Privilege == PrivilegeNobody {
			id = ProfileNobody
		}
	}
	c, ok := find(id)
	if !ok {
		return c, fmt.Errorf("%w: no run profile %q", ErrNotFound, id)
	}
	if c.Kind != ProfileSSH {
		return c, fmt.Errorf("%w: %s is for the cluster, and this script targets nodes", ErrInvalid, c.Name)
	}
	if !c.Reaches(fleet) {
		return c, fmt.Errorf("%w: %s does not reach fleet %s", ErrInvalid, c.Name, fleet)
	}
	if h.Privilege == PrivilegeElevated && c.Privilege != PrivilegeElevated {
		return c, fmt.Errorf("%w: the script declares privilege elevated, and %s gives only %s", ErrInvalid, c.Name, PrivilegeName(c.Privilege))
	}
	if !fp.Allows(c.Privilege) {
		return c, fmt.Errorf("%w: %s gives %s, more than this fleet allows (%s)", ErrInvalid, c.Name, PrivilegeName(c.Privilege), PrivilegeName(fp.CeilingOrDefault()))
	}
	return c, nil
}
