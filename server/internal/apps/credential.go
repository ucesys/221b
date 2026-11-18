package apps

import (
	"context"
	"strings"

	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Credentials (0.12.1, CR-11, CR-12) ---

// AuthFor is how an application's git repository is fetched: with the
// credential its record names, resolved for the repository's address; a
// record the upgrade has not switched yet uses the token kept with it
// (legacy), as before. A repository's chart is signed in to by its
// repository instead.
func AuthFor(ctx context.Context, creds *credentials.Service, rec Record, legacy string) (GitAuth, error) {
	if rec.Source.FromRepository() {
		return GitAuth{}, nil
	}
	if rec.CredMigrated || rec.Credential != "" {
		return CredentialAuth(ctx, creds, rec.Credential, rec.Source.Repo)
	}
	if legacy != "" {
		return GitAuth{Password: legacy}, nil
	}
	return GitAuth{}, nil
}

// CredentialAuth resolves a credential for a git repository; "" is none.
func CredentialAuth(ctx context.Context, creds *credentials.Service, id, repo string) (GitAuth, error) {
	if id == "" {
		return GitAuth{}, nil
	}
	use, err := creds.Resolve(ctx, id, credentials.UseGit, repo)
	if err != nil {
		return GitAuth{}, err
	}
	return GitAuthOf(use), nil
}

// RepoOwner is the endpoint a git repository's token was for, as the
// upgrade names it: its scheme, host and owner (https://github.com/org).
func RepoOwner(repo string) string {
	scheme, rest, ok := strings.Cut(repo, "://")
	if !ok {
		return repo
	}
	host, p, _ := strings.Cut(rest, "/")
	owner, _, _ := strings.Cut(p, "/")
	if owner == "" {
		return scheme + "://" + host
	}
	return scheme + "://" + host + "/" + owner
}

// Label names an application for Administration › Credentials: "app
// team/shop", with its cluster when it is not the console's own ("local").
func Label(rec Record) string {
	l := "app " + rec.Namespace + "/" + rec.Name
	if rec.Cluster != "local" {
		l += " on " + rec.Cluster
	}
	return l
}

// CredentialUses lists the applications that name a credential, for
// Administration › Credentials' Used by and for refusing to remove one in use.
func CredentialUses(store Store) credentials.UsersFunc {
	return func(ctx context.Context) ([]credentials.Ref, error) {
		recs, err := store.List(ctx)
		if err != nil {
			return nil, err
		}
		var out []credentials.Ref
		for _, rec := range recs {
			// One naming a git repository of Administration › Settings › Repositories uses
			// the repository's (RP-03), which lists it.
			if rec.Credential != "" && !(isGit(rec.Source) && rec.Source.Repository != "") {
				out = append(out, credentials.Ref{Credential: rec.Credential, What: Label(rec), Where: "Helm › " + rec.Namespace + "/" + rec.Name + " › Source"})
			}
		}
		return out, nil
	}
}

// CredentialUses lists the repositories that name a credential.
func (r *Repos) CredentialUses(ctx context.Context) ([]credentials.Ref, error) {
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []credentials.Ref
	for _, x := range list {
		if x.Credential != "" {
			out = append(out, credentials.Ref{Credential: x.Credential, What: KindWord(x.Kind) + " " + x.Name, Where: "Administration › Settings › Repositories"})
		}
	}
	return out, nil
}
