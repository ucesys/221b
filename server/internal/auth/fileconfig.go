package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ucesys/221b/server/internal/roles"
)

// FileConfig is the file the chart renders for the roles and the directory
// (Phase 6.1): everything that is a list or a tree, which flags cannot say.
// Secrets are not in it: the search account's password and the setup token
// come through the environment.
type FileConfig struct {
	// Lists are the groups of each role, for LDAP and OIDC (RB-20).
	Lists RoleLists `json:"lists"`
	// LocalGroups are defined for local users (RB-50).
	LocalGroups []LocalGroup `json:"localGroups"`
	// Templates say which definition of Admin and SuperAdmin is installed.
	Templates struct {
		Admin      roles.Template `json:"admin"`
		SuperAdmin roles.Template `json:"superAdmin"`
	} `json:"templates"`
	// RBACEnforce is off or strict (RB-36).
	RBACEnforce string `json:"rbacEnforce"`
	// Prefixes override the defaults of RB-15.
	Prefixes struct {
		Local string `json:"local"`
		LDAP  string `json:"ldap"`
	} `json:"prefixes"`
	LDAP *LDAPFile `json:"ldap"`
}

// LDAPFile is auth.ldap.
type LDAPFile struct {
	URL      string `json:"url"`
	StartTLS bool   `json:"startTLS"`
	// CAFile is the path the chart mounts the CA at.
	CAFile string `json:"caFile"`
	BindDN string `json:"bindDN"`
	// UserSearch finds a person by what they type; %s is it.
	UserSearch struct {
		Base         string `json:"base"`
		Filter       string `json:"filter"`
		UsernameAttr string `json:"usernameAttr"`
		NameAttr     string `json:"nameAttr"`
		EmailAttr    string `json:"emailAttr"`
	} `json:"userSearch"`
	// GroupSearch finds the groups of a person or a group; %s is the DN.
	GroupSearch struct {
		Base       string `json:"base"`
		Filter     string `json:"filter"`
		NameAttr   string `json:"nameAttr"`
		MemberAttr string `json:"memberAttr"`
		Nested     *bool  `json:"nested"`
	} `json:"groupSearch"`
	PageSize     int    `json:"pageSize"`
	SyncInterval string `json:"syncInterval"`
}

// SyncEvery is the sync interval, an hour when unset.
func (l LDAPFile) SyncEvery() (time.Duration, error) {
	if l.SyncInterval == "" {
		return time.Hour, nil
	}
	d, err := time.ParseDuration(l.SyncInterval)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("auth.ldap.syncInterval %q must be a duration of at least a minute, such as 1h", l.SyncInterval)
	}
	return d, nil
}

// ReadFileConfig reads and checks the file. A missing path is the defaults.
func ReadFileConfig(path string) (FileConfig, error) {
	var f FileConfig
	if path == "" {
		return f, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("reading %s: %w", path, err)
	}
	for id, groups := range f.Lists {
		if !roles.Valid(string(id)) {
			return f, fmt.Errorf("%s: %q is not a role; use viewer, superViewer, admin or superAdmin", path, id)
		}
		for _, g := range groups {
			if g == "" {
				return f, fmt.Errorf("%s: an empty group under %s", path, id)
			}
		}
	}
	for _, t := range []roles.Template{f.Templates.Admin, f.Templates.SuperAdmin} {
		if t != "" && !t.Valid() {
			return f, fmt.Errorf("%s: a role template must be full or readOnly, not %q", path, t)
		}
	}
	if f.RBACEnforce != "" && f.RBACEnforce != "off" && f.RBACEnforce != "strict" {
		return f, fmt.Errorf("%s: rbac.enforce must be off or strict, not %q", path, f.RBACEnforce)
	}
	return f, nil
}

// RoleTemplates are the definitions in use.
func (f FileConfig) RoleTemplates() roles.Templates {
	return roles.Templates{Admin: f.Templates.Admin, SuperAdmin: f.Templates.SuperAdmin}
}

// Apply puts the file's roles, groups and prefixes on a configuration.
func (f FileConfig) Apply(c *Config) {
	c.Lists = f.Lists
	c.LocalGroups = f.LocalGroups
	if f.Prefixes.Local != "" {
		c.Prefixes.Local = f.Prefixes.Local
	}
	if f.Prefixes.LDAP != "" {
		c.Prefixes.LDAP = f.Prefixes.LDAP
	}
}
