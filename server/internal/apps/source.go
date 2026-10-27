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

// RefKind says what a ref names.
const (
	RefBranch = "branch"
	RefTag    = "tag"
)

// Source is where an application's chart comes from: a git repository
// reachable over HTTPS, a directory in it, and a branch or a tag.
type Source struct {
	// Repo is the repository's HTTPS URL (https://github.com/org/app).
	Repo string `json:"repo"`
	// Path is the chart's directory in the repository ("" for the root).
	Path string `json:"path,omitempty"`
	// Ref is the branch or tag deployed.
	Ref string `json:"ref"`
	// RefKind is branch or tag.
	RefKind string `json:"refKind"`
}

// String is the source as the table and the audit log show it:
// github.com/org/app/charts/app @ v1.2.0.
func (s Source) String() string {
	repo := strings.TrimPrefix(strings.TrimPrefix(s.Repo, "https://"), "http://")
	repo = strings.TrimSuffix(repo, ".git")
	if s.Path != "" {
		repo += "/" + s.Path
	}
	return repo + " @ " + s.Ref
}

var refName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]*$`)

// AllowLocalRepos lets a repository be a directory on the server's own
// disk: for tests, which clone a repository they made. Never set by the
// server.
var AllowLocalRepos bool

// Normalize cleans a source as typed and says what is wrong with it: the
// repository must be an HTTPS URL (github.com/org/app is completed), the
// path must stay inside the repository, and the ref must be a plain name.
func Normalize(s Source) (Source, error) {
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
		} else if strings.HasPrefix(p, "../") || p == ".." || strings.Contains(p, "\x00") {
			return s, errors.New("the chart path must be inside the repository")
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

// Record is what the console keeps about an application (HA-03).
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
	// HasToken says a credential for the repository is saved with the
	// record; the token itself never leaves the server.
	HasToken  bool      `json:"hasToken,omitempty"`
	CreatedBy string    `json:"createdBy,omitempty"`
	Created   time.Time `json:"created"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	Updated   time.Time `json:"updated"`
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
