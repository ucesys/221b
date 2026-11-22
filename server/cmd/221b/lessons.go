package main

import (
	"errors"
	"log/slog"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
)

// --- Findings, exceptions and RCAs (Phase 12, ADR-108 to ADR-110) ---

// openLessons builds the keeper of the accepted exceptions: kept beside
// the cases, through the case work's backend (the database's exceptions),
// and written to Mycroft's wiki too once one is kept. backend nil keeps
// them in memory (a test).
func openLessons(backend kv.Backend, auditLog *audit.Log, wiki *knowledge.Service, log *slog.Logger) *lessons.Service {
	if backend == nil {
		backend = &kv.Memory{}
	}
	svc := lessons.New(lessons.Options{Backend: backend, Audit: auditLog, Logger: log.With("component", "lessons")})
	if wiki != nil {
		svc.SetWiki(wiki, func(err error) bool { return errors.Is(err, knowledge.ErrNoStore) })
	}
	return svc
}

// openFixes builds the keeper of the fixes (wave 6g, CF-50): through the
// case work's backend beside the exceptions (the database's fixes).
// backend nil keeps them in memory (a test).
func openFixes(backend kv.Backend, auditLog *audit.Log, log *slog.Logger) *fixes.Service {
	if backend == nil {
		backend = &kv.Memory{}
	}
	return fixes.New(fixes.Options{Backend: backend, Audit: auditLog, Logger: log.With("component", "fixes")})
}
