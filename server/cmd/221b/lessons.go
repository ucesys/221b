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

// openLessons builds the keeper of the accepted exceptions and the RCAs:
// kept beside the cases, in the casework ConfigMap (its keys
// exceptions.json and rcas.json), and written to Mycroft's wiki too once
// one is kept. backend nil keeps them in memory.
func openLessons(backend kv.Backend, auditLog *audit.Log, wiki *knowledge.Service, log *slog.Logger) *lessons.Service {
	if backend == nil {
		backend = &kv.Memory{}
		log.Warn("the accepted exceptions and RCAs are kept in memory and lost on restart (set -casework-configmap to keep them)")
	}
	svc := lessons.New(lessons.Options{Backend: backend, Audit: auditLog, Logger: log.With("component", "lessons")})
	if wiki != nil {
		svc.SetWiki(wiki, func(err error) bool { return errors.Is(err, knowledge.ErrNoStore) })
	}
	return svc
}

// openFixes builds the keeper of the fixes (wave 6g, CF-50): in the
// casework ConfigMap beside the exceptions (its key fixes.json). backend
// nil keeps them in memory.
func openFixes(backend kv.Backend, auditLog *audit.Log, log *slog.Logger) *fixes.Service {
	if backend == nil {
		backend = &kv.Memory{}
		log.Warn("the fixes are kept in memory and lost on restart (set -casework-configmap to keep them)")
	}
	return fixes.New(fixes.Options{Backend: backend, Audit: auditLog, Logger: log.With("component", "fixes")})
}
