package clusters

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ucesys/221b/server/internal/version"
)

// RestConfig builds the client configuration for a connection from its
// spec and credential.
func RestConfig(s Spec, c Credential) (*rest.Config, error) {
	// What the connection lacks is said whole: a connection whose Secret
	// is gone lacks its CA certificate as well as its token, and both
	// are given again.
	if missing := lacks(s, c); len(missing) > 0 {
		return nil, errors.New("the connection has no " + strings.Join(missing, " and no "))
	}
	var cfg *rest.Config
	switch s.Auth {
	case AuthToken:
		if strings.TrimSpace(c.Token) == "" {
			return nil, errors.New("the connection has no token")
		}
		cfg = &rest.Config{Host: s.Server, BearerToken: strings.TrimSpace(c.Token)}
	case AuthKubeconfig:
		if strings.TrimSpace(c.Kubeconfig) == "" {
			return nil, errors.New("the connection has no kubeconfig")
		}
		kc, err := clientcmd.Load([]byte(c.Kubeconfig))
		if err != nil {
			return nil, fmt.Errorf("reading the kubeconfig: %w", err)
		}
		overrides := &clientcmd.ConfigOverrides{}
		if c.Context != "" {
			overrides.CurrentContext = c.Context
		}
		cfg, err = clientcmd.NewNonInteractiveClientConfig(*kc, overrides.CurrentContext, overrides, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("reading the kubeconfig: %w", err)
		}
		if cfg.ExecProvider != nil || cfg.AuthProvider != nil {
			return nil, errors.New("the kubeconfig signs in through a plugin, which the console cannot run")
		}
		if im := cfg.Impersonate; im.UserName != "" || len(im.Groups) > 0 || im.UID != "" || len(im.Extra) > 0 {
			return nil, errors.New("the kubeconfig acts as another user (as:), which the console cannot do beside reading as each person")
		}
		// Files would be read on the console's machine: its own token, sent
		// to whatever server the kubeconfig names.
		if cfg.BearerTokenFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" {
			return nil, errors.New("the kubeconfig refers to files; paste one with the credentials inline")
		}
		if s.Server != "" {
			cfg.Host = s.Server
		}
		// client-go keeps a kubeconfig's credentials back from a plain-HTTP
		// server, as kubectl does. The only such server is a simulator, whose
		// test asks for its token all the same.
		if strings.HasPrefix(cfg.Host, "http://") && cfg.BearerToken == "" {
			name := c.Context
			if name == "" {
				name = kc.CurrentContext
			}
			if ctx := kc.Contexts[name]; ctx != nil {
				if user := kc.AuthInfos[ctx.AuthInfo]; user != nil {
					cfg.BearerToken = user.Token
					if user.Exec != nil || user.AuthProvider != nil {
						return nil, errors.New("the kubeconfig signs in through a plugin, which the console cannot run")
					}
				}
			}
		}
	case AuthCert:
		if c.CertPEM == "" || c.KeyPEM == "" {
			return nil, errors.New("the connection has no client certificate and key")
		}
		cfg = &rest.Config{Host: s.Server, TLSClientConfig: rest.TLSClientConfig{CertData: []byte(c.CertPEM), KeyData: []byte(c.KeyPEM)}}
	case AuthOIDC:
		return nil, errors.New("OIDC sign-in to a cluster is not available yet")
	case AuthNone:
		cfg = &rest.Config{Host: s.Server}
	default:
		return nil, fmt.Errorf("unknown sign-in method %q", s.Auth)
	}
	switch s.CA {
	case CAPEM:
		if strings.TrimSpace(c.CAPEM) == "" && s.Auth != AuthKubeconfig {
			return nil, errors.New("the connection has no CA certificate")
		}
		if strings.TrimSpace(c.CAPEM) != "" {
			cfg.TLSClientConfig.CAData = []byte(c.CAPEM)
			cfg.TLSClientConfig.CAFile = ""
			cfg.TLSClientConfig.Insecure = false
		}
	case CASystem:
		cfg.TLSClientConfig.CAData, cfg.TLSClientConfig.CAFile = nil, ""
		cfg.TLSClientConfig.Insecure = false
	case CASkip:
		cfg.TLSClientConfig.CAData, cfg.TLSClientConfig.CAFile = nil, ""
		cfg.TLSClientConfig.Insecure = true
	}
	cfg.UserAgent = "221b/" + version.Version
	cfg.QPS, cfg.Burst = 50, 100
	return cfg, nil
}

// lacks names what a connection needs to sign in that its credential
// does not hold.
func lacks(s Spec, c Credential) []string {
	var out []string
	switch s.Auth {
	case AuthToken:
		if strings.TrimSpace(c.Token) == "" {
			out = append(out, "token")
		}
	case AuthKubeconfig:
		if strings.TrimSpace(c.Kubeconfig) == "" {
			out = append(out, "kubeconfig")
		}
	case AuthCert:
		if c.CertPEM == "" || c.KeyPEM == "" {
			out = append(out, "client certificate and key")
		}
	}
	if s.CA == CAPEM && s.Auth != AuthKubeconfig && strings.TrimSpace(c.CAPEM) == "" {
		out = append(out, "CA certificate")
	}
	return out
}
