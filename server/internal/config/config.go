// Package config reads the server's settings from flags, with environment
// variables as defaults so the Helm chart can set them.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the server needs to start.
type Config struct {
	// Listen is the address the HTTP server binds, such as ":8080".
	Listen string
	// ClusterName is the name shown in the top bar. Kubernetes has no
	// reliable name for itself, so the chart passes one in.
	ClusterName string
	// Kubeconfig and Context select a cluster when the server runs outside
	// one, for development. Both are ignored inside a cluster.
	Kubeconfig string
	Context    string
	// APIServer connects to a plain URL without credentials. It is meant for
	// the simulator (221b-sim) and for tests.
	APIServer string
	// Refresh is how often browsers are updated and metrics-server read,
	// until someone changes it in the console's Settings.
	Refresh time.Duration
	// SettingsConfigMap is the "namespace/name" of the ConfigMap the
	// settings are kept in. Empty keeps them in memory until a restart.
	SettingsConfigMap string
	// ConnectionsSecrets is the "namespace/prefix" of the Secrets the
	// cluster connections added in the console are kept in, one Secret
	// <prefix>-cluster-<id> each. Empty keeps them in memory until a restart.
	ConnectionsSecrets string
	// SherlockAPIURL is the Claude API's address (Phase 5); a test points
	// it at a fake.
	SherlockAPIURL string
	// SherlockLockProvider keeps the settings from changing Sherlock's
	// provider: the chart's sherlock.lockProvider.
	SherlockLockProvider bool
	// SherlockProvider is the provider when the settings name none, as JSON
	// (the chart's sherlock.provider); with SherlockLockProvider the only one.
	SherlockProvider string
	// LocalMode is what Watson may do in the cluster the console runs in
	// (Phase 6): read, or write to allow changes after approval.
	LocalMode string
	// SherlockSecret is the "namespace/name" of the Secret the Claude API
	// key is kept in once an administrator saves it. Empty keeps it in
	// memory until a restart.
	SherlockSecret string
	// SherlockAPIKey is a key the chart passes in (sherlock.existingSecret),
	// from the environment only; it wins over a saved one.
	SherlockAPIKey string
	// KubeletStats reads each node's boot time from its kubelet, for the
	// uptime in the node details. It needs get on nodes/proxy.
	KubeletStats bool
	// SecretsRead watches Secrets, for their keys and sizes and for Helm
	// releases. Off, no Secret is ever read and the Helm tab is hidden.
	SecretsRead bool
	// SecretsReveal lets Admins and SuperAdmins see a Secret's values on
	// request (Phase 10). Off, they can only write them.
	SecretsReveal bool
	// HealthInterval is how often the API server is checked.
	HealthInterval time.Duration
	// LogLevel is debug, info, warn or error. LogFormat is json or text.
	LogLevel  string
	LogFormat string
	// AllowedHosts are host names the server answers to besides localhost
	// and IP addresses, such as its Service's DNS names.
	AllowedHosts []string

	// AuthMode is how people sign in (P3-01): none, oidc, local or ldap.
	AuthMode string
	// AuthConfigFile is the JSON file the chart renders for the roles: which
	// groups hold which role, the local groups, the LDAP server, which
	// definition of Admin and SuperAdmin is installed, rbac.enforce
	// (Phase 6.1). Empty for the defaults.
	AuthConfigFile string
	// ToolboxConfigFile is the JSON file the chart renders from its
	// toolbox values (TB-48); empty uses the defaults.
	ToolboxConfigFile string
	// UsersSecret is namespace/name of the Secret that keeps the users
	// created in the console; empty keeps them in memory (RB-46).
	UsersSecret string
	// SetupToken must be typed on the first-run page when set (RB-27). It is
	// read from the environment only, never from an argument.
	SetupToken string
	// LDAPBindPassword is the search account's password, from the
	// environment only (RB-19).
	LDAPBindPassword string
	// OIDC settings, for AuthMode oidc.
	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCScopes        []string
	OIDCRedirectURL   string
	OIDCUsernameClaim string
	OIDCGroupsClaim   string
	OIDCProviderName  string
	// LocalUsersFile holds the local accounts, for AuthMode local.
	LocalUsersFile string
	// Admins and AdminGroups say who manages clusters and settings (P3-03).
	Admins      []string
	AdminGroups []string
	// SessionIdle and SessionMax bound a session's life.
	SessionIdle time.Duration
	SessionMax  time.Duration
	// AuditWebhookURL receives every audit record; AuditWebhookToken is
	// sent with it as a bearer token (P3-10).
	AuditWebhookURL   string
	AuditWebhookToken string
	// ImpersonateUserPrefix and ImpersonateGroupPrefix are what the API
	// servers' OIDC configuration puts before user names and groups
	// (--oidc-username-prefix, --oidc-groups-prefix), so the console reads
	// as the same user RBAC was written for (P3-02).
	ImpersonateUserPrefix  string
	ImpersonateGroupPrefix string
	// AccessTTL is how long an answer about what a person may see stands
	// before Kubernetes is asked again.
	AccessTTL time.Duration
	// HTTPSRedirect sends plain-HTTP requests to HTTPS, behind a
	// TLS-terminating Ingress (P3-11).
	HTTPSRedirect bool
	// LeaderLease is the "namespace/name" of the Lease the replicas elect
	// the one that runs Sherlock and Watson with (P7-02). Empty: a single
	// replica, always the leader.
	LeaderLease string
	// PodName and PodIP name this replica, from the downward API; the
	// leader's address is PodIP and the listen port.
	PodName string
	PodIP   string
	// ProxyToken is shared by the replicas, so the leader believes the
	// client address a follower forwards; from the environment only.
	ProxyToken string
	// WarmStandby keeps a replica that does not lead watching the clusters,
	// so it takes over at once (ADR-70); off, it holds nothing and lists
	// them when it wins the Lease.
	WarmStandby bool
}

// Parse reads the configuration from args (without the program name).
func Parse(args []string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("221b", flag.ContinueOnError)
	fs.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "address to serve HTTP on")
	fs.StringVar(&c.ClusterName, "cluster-name", env("CLUSTER_NAME", "cluster"), "name of the cluster shown in the top bar")
	fs.StringVar(&c.Kubeconfig, "kubeconfig", env("KUBECONFIG_PATH", ""), "path to a kubeconfig file (outside a cluster only)")
	fs.StringVar(&c.Context, "context", env("KUBE_CONTEXT", ""), "kubeconfig context to use (outside a cluster only)")
	fs.StringVar(&c.APIServer, "api-server", env("API_SERVER", ""), "connect to this API server URL without credentials (simulator and tests)")
	fs.DurationVar(&c.Refresh, "refresh", envDuration("REFRESH", 3*time.Second), "how often to update browsers and read CPU and memory in use, until changed in Settings")
	fs.StringVar(&c.SettingsConfigMap, "settings-configmap", env("SETTINGS_CONFIGMAP", ""), "namespace/name of the ConfigMap to keep the settings in (empty: in memory)")
	fs.StringVar(&c.ConnectionsSecrets, "connections-secrets", env("CONNECTIONS_SECRETS", ""), "namespace/prefix of the Secrets to keep cluster connections in, one <prefix>-cluster-<id> each (empty: in memory, lost on restart)")
	fs.BoolVar(&c.KubeletStats, "kubelet-stats", envBool("KUBELET_STATS", true), "read node boot times from the kubelets, for uptime (needs get on nodes/proxy)")
	fs.BoolVar(&c.SecretsRead, "secrets-read", envBool("SECRETS_READ", true), "read Secrets for their keys and sizes and for Helm releases; values are never shown (off hides the Helm tab)")
	fs.BoolVar(&c.SecretsReveal, "secrets-reveal", envBool("SECRETS_REVEAL", false), "let Admins and SuperAdmins see a Secret's values, one key at a time, audited (needs -secrets-read); off, values can only be written")
	fs.DurationVar(&c.HealthInterval, "health-interval", envDuration("HEALTH_INTERVAL", 10*time.Second), "how often to check the API server")
	fs.StringVar(&c.LogLevel, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error")
	fs.StringVar(&c.LogFormat, "log-format", env("LOG_FORMAT", "json"), "json or text")
	hosts := fs.String("allowed-hosts", env("ALLOWED_HOSTS", ""), "comma-separated host names to answer to, besides localhost and IP addresses")
	fs.StringVar(&c.AuthMode, "auth-mode", env("AUTH_MODE", "none"), "how people sign in: oidc, ldap, local (accounts kept by the console) or none (no sign-in; everyone is an admin)")
	fs.StringVar(&c.ToolboxConfigFile, "toolbox-config", env("TOOLBOX_CONFIG_FILE", ""), "a JSON file with the toolbox values: tools, images and limits (the chart renders it)")
	fs.StringVar(&c.AuthConfigFile, "auth-config", env("AUTH_CONFIG_FILE", ""), "a JSON file with the roles' groups, the local groups and the LDAP server (the chart renders it)")
	fs.StringVar(&c.UsersSecret, "users-secret", env("USERS_SECRET", ""), "namespace/name of the Secret to keep users created in the console in (empty: with -connections-secrets, <prefix>-users there; else in memory, lost on restart)")
	c.SetupToken = os.Getenv("SETUP_TOKEN")
	c.LDAPBindPassword = os.Getenv("LDAP_BIND_PASSWORD")
	fs.StringVar(&c.OIDCIssuer, "oidc-issuer", env("OIDC_ISSUER", ""), "the OIDC provider's issuer URL")
	fs.StringVar(&c.OIDCClientID, "oidc-client-id", env("OIDC_CLIENT_ID", ""), "the OIDC client ID")
	fs.StringVar(&c.OIDCClientSecret, "oidc-client-secret", env("OIDC_CLIENT_SECRET", ""), "the OIDC client secret (better set through the environment)")
	scopes := fs.String("oidc-scopes", env("OIDC_SCOPES", "openid,profile,email"), "comma-separated scopes to ask for; add groups when the provider needs it")
	fs.StringVar(&c.OIDCRedirectURL, "oidc-redirect-url", env("OIDC_REDIRECT_URL", ""), "the callback URL registered with the provider (empty: https://<host>/signin/callback from each request)")
	fs.StringVar(&c.OIDCUsernameClaim, "oidc-username-claim", env("OIDC_USERNAME_CLAIM", "email"), "the claim that names a person to Kubernetes and the audit log")
	fs.StringVar(&c.OIDCGroupsClaim, "oidc-groups-claim", env("OIDC_GROUPS_CLAIM", "groups"), "the claim that lists a person's groups")
	fs.StringVar(&c.OIDCProviderName, "oidc-provider-name", env("OIDC_PROVIDER_NAME", ""), "what the sign-in button calls the provider")
	fs.StringVar(&c.LocalUsersFile, "local-users", env("LOCAL_USERS_FILE", ""), "a JSON or YAML file of local accounts defined in values (username or email, name, groups, roles, passwordHash)")
	admins := fs.String("admins", env("ADMINS", ""), "comma-separated usernames that manage clusters and settings")
	adminGroups := fs.String("admin-groups", env("ADMIN_GROUPS", ""), "comma-separated groups that manage clusters and settings")
	fs.DurationVar(&c.SessionIdle, "session-idle", envDuration("SESSION_IDLE", 8*time.Hour), "sign a session out after this long without a request")
	fs.DurationVar(&c.SessionMax, "session-max", envDuration("SESSION_MAX", 7*24*time.Hour), "sign a session out this long after sign-in whatever happens")
	fs.StringVar(&c.AuditWebhookURL, "audit-webhook", env("AUDIT_WEBHOOK_URL", ""), "POST every audit record to this URL as JSON")
	fs.StringVar(&c.AuditWebhookToken, "audit-webhook-token", env("AUDIT_WEBHOOK_TOKEN", ""), "bearer token sent with audit webhook requests")
	fs.StringVar(&c.ImpersonateUserPrefix, "impersonate-user-prefix", env("IMPERSONATE_USER_PREFIX", ""), "put before user names when reading as a person: the API servers' --oidc-username-prefix")
	fs.StringVar(&c.ImpersonateGroupPrefix, "impersonate-group-prefix", env("IMPERSONATE_GROUP_PREFIX", ""), "put before group names when reading as a person: the API servers' --oidc-groups-prefix")
	fs.DurationVar(&c.AccessTTL, "access-ttl", envDuration("ACCESS_TTL", 2*time.Minute), "how long an answer about what a person may see stands before Kubernetes is asked again")
	fs.StringVar(&c.SherlockAPIURL, "sherlock-api-url", env("SHERLOCK_API_URL", "https://api.anthropic.com"), "the Claude API's address, for Sherlock, when the settings name no other provider")
	fs.StringVar(&c.SherlockProvider, "sherlock-provider", env("SHERLOCK_PROVIDER", ""), "Sherlock's provider when the settings name none, as JSON: {\"kind\":\"bedrock\",\"region\":\"eu-west-1\"}")
	fs.BoolVar(&c.SherlockLockProvider, "sherlock-lock-provider", env("SHERLOCK_LOCK_PROVIDER", "") == "true", "keep the settings from changing Sherlock's provider and endpoint")
	fs.StringVar(&c.LocalMode, "local-mode", env("LOCAL_MODE", "read"), "what Watson may do in the cluster the console runs in: read, or write to allow changes after approval")
	fs.StringVar(&c.SherlockSecret, "sherlock-secret", env("SHERLOCK_SECRET", ""), "namespace/name of the Secret to keep the Claude API key in (empty: in memory, lost on restart)")
	c.SherlockAPIKey = os.Getenv("SHERLOCK_API_KEY")
	fs.BoolVar(&c.HTTPSRedirect, "https-redirect", envBool("HTTPS_REDIRECT", false), "send plain-HTTP requests (by X-Forwarded-Proto) to HTTPS, behind an Ingress with TLS")
	fs.StringVar(&c.LeaderLease, "leader-lease", env("LEADER_LEASE", ""), "namespace/name of the Lease the replicas elect the one that runs Sherlock and Watson with (empty: a single replica)")
	c.PodName = os.Getenv("POD_NAME")
	c.PodIP = os.Getenv("POD_IP")
	c.ProxyToken = os.Getenv("PROXY_TOKEN")
	fs.BoolVar(&c.WarmStandby, "warm-standby", envBool("WARM_STANDBY", true), "keep a replica that does not lead watching the clusters, so it takes over at once (false: it lists them when it wins the Lease)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	for _, h := range strings.Split(*hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.AllowedHosts = append(c.AllowedHosts, h)
		}
	}
	c.OIDCScopes = splitList(*scopes)
	c.Admins = splitList(*admins)
	c.AdminGroups = splitList(*adminGroups)
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return c, c.validate()
}

// splitList reads a comma-separated list, trimming and dropping blanks.
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (c Config) validate() error {
	var errs []error
	if c.LocalMode != "read" && c.LocalMode != "write" {
		errs = append(errs, fmt.Errorf("-local-mode must be read or write, not %q", c.LocalMode))
	}
	if c.ClusterName == "" {
		errs = append(errs, errors.New("cluster name must not be empty"))
	}
	switch c.AuthMode {
	case "none", "local":
	case "ldap":
		if c.AuthConfigFile == "" {
			errs = append(errs, errors.New("LDAP sign-in needs -auth-config, the file with the server and the groups"))
		}
	case "oidc":
		if c.OIDCIssuer == "" || c.OIDCClientID == "" {
			errs = append(errs, errors.New("OIDC sign-in needs -oidc-issuer and -oidc-client-id"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown auth mode %q: use oidc, ldap, local or none", c.AuthMode))
	}
	if c.SessionIdle < time.Minute || c.SessionMax < time.Minute {
		errs = append(errs, errors.New("session lifetimes must be at least 1m"))
	}
	if c.Refresh < time.Second || c.Refresh > 10*time.Minute {
		errs = append(errs, errors.New("refresh must be between 1s and 10m"))
	}
	if c.HealthInterval < time.Second {
		errs = append(errs, errors.New("health interval must be at least 1s"))
	}
	if c.LeaderLease != "" {
		if ns, name, ok := strings.Cut(c.LeaderLease, "/"); !ok || ns == "" || name == "" {
			errs = append(errs, fmt.Errorf("-leader-lease must be namespace/name, got %q", c.LeaderLease))
		}
		if c.PodName == "" || c.PodIP == "" {
			errs = append(errs, errors.New("-leader-lease needs POD_NAME and POD_IP from the downward API"))
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("unknown log level %q", c.LogLevel))
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("unknown log format %q", c.LogFormat))
	}
	return errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
