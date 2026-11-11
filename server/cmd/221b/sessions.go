package main

import (
	"context"
	"time"

	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// The toolbox's sessions (Phase 9).

// wireSessions starts the toolbox's sessions.
func (w *wiring) wireSessions(ctx context.Context) {
	log := w.log
	sessionsMgr := sessions.New(sessions.Options{
		Config:  w.toolboxCfg,
		SSH:     w.sshSvc,
		Audit:   w.auditLog,
		Logger:  log.With("component", "sessions"),
		History: &sessions.KVHistory{Backend: w.spendBackend, Key: sessions.HistoryKey, Keep: w.toolboxCfg.History.Keep, MaxBytes: historyMaxBytes, Logger: log.With("component", "sessions")},
	})
	go sessionsMgr.Run(ctx)
	if w.toolboxCfg.Transcripts == toolbox.TranscriptsPVC {
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				sessions.PruneTranscripts(w.toolboxCfg.TranscriptDir, w.toolboxCfg.Retention.D(), time.Now())
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	w.sessionsMgr = sessionsMgr
}
