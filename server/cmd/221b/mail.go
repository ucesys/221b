package main

import (
	"context"
	"log/slog"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/mail"
)

// --- Email about cases (EM-01 to EM-10, ADR-158) ---

// openMail opens Settings › Email: the settings in the casework ConfigMap
// (mail.ConfigKey), the password in the Secret <release>-cases beside the
// notification channels' addresses (memory without one), and the links
// from Settings › Watson's console address.
func openMail(cfg config.Config, restCfg *rest.Config, backend kv.Backend, cardStore *cards.Store, auditLog *audit.Log, log *slog.Logger) *mail.Service {
	var secrets mail.SecretStore
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(restCfg, ns, prefix+"-cases", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "cases", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			log.Warn("the email password is kept in memory: its Secret could not be opened", "err", err)
		} else {
			secrets = sk
		}
	}
	return mail.New(mail.Options{
		Backend: backend,
		Secrets: secrets,
		Link: func(ctx context.Context) string {
			if cardStore == nil {
				return ""
			}
			v, err := cardStore.Notifications(ctx)
			if err != nil {
				return ""
			}
			return v.Link
		},
		Audit:  auditLog,
		Logger: log.With("component", "mail"),
	})
}
