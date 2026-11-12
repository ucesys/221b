package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/knowledge"
)

// --- The clean start (Phase 12, wave 6, CF-23) ---

// runCleanStart runs the clean start once this replica leads, when the
// chart's cases.cleanStart.token asks for one (casework.CleanStart says
// what it deletes and keeps). It gives up when ctx ends.
func runCleanStart(ctx context.Context, cfg config.Config, st casework.CleanStores, wiki *knowledge.Service, leads func() bool, auditLog *audit.Log, log *slog.Logger) {
	if cfg.CleanStartToken == "" {
		return
	}
	if wiki != nil {
		st.Wiki = func(ctx context.Context, dry bool) ([]string, error) {
			gone, err := wiki.WipeMycroft(ctx, dry)
			if errors.Is(err, knowledge.ErrNoStore) {
				return nil, nil
			}
			return gone, err
		}
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for !leads() {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := casework.CleanStart(cctx, st, cfg.CleanStartToken, cfg.CleanStartDryRun, time.Now(), log.With("component", "clean-start"), auditLog)
	switch {
	case err != nil:
		log.Error("the clean start failed; it runs again on the next start", "err", err)
	case res.Done && res.DryRun:
		log.Info("the clean start's dry run is done: nothing was deleted; set cases.cleanStart.dryRun false to delete", "items", len(res.Items))
	case res.Done:
		log.Info("the Cases area started clean", "items", len(res.Items))
	}
}

// runFixesImport saves, once, on the leader, the fixes of the cases closed
// as Fixed before fixes were kept (CF-54).
func runFixesImport(ctx context.Context, svc *casework.Service, leads func() bool, log *slog.Logger) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for !leads() {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := svc.ImportFixes(ictx); err != nil {
		log.Warn("the fixes of the cases closed as fixed before could not all be saved; it runs again on the next start", "err", err)
	}
}
