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
	// The ended sessions' records are rows of the database (Phase 13).
	// What a release before 0.13 kept of them in a ConfigMap (the settings'
	// or their own: the copy's source, read for nothing else) is copied
	// over once.
	pg := &sessions.PGHistory{DB: w.database, Keep: w.toolboxCfg.History.Keep, Logger: log.With("component", "sessions")}
	if from := w.settingsSource; hasSource(from) {
		w.readFrom(from, sessions.HistoryKey, sessions.HistoryKey)
		kept := &sessions.KVHistory{Backend: from, Key: sessions.HistoryKey, Keep: w.toolboxCfg.History.Keep, MaxBytes: historyMaxBytes, Logger: log.With("component", "sessions")}
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		n, err := pg.Import(ictx, kept, "configmap key "+sessions.HistoryKey)
		cancel()
		switch {
		case err != nil:
			log.Warn("copying the Toolbox's session records from their ConfigMap failed; the next start tries again", "err", err)
		case n > 0:
			log.Info("copied the Toolbox's session records from their ConfigMap into the database", "records", n)
		}
	}
	var history sessions.Archive = pg
	w.sessionRecords = history
	sessionsMgr := sessions.New(sessions.Options{
		Config:  w.toolboxCfg,
		SSH:     w.sshSvc,
		Audit:   w.auditLog,
		Logger:  log.With("component", "sessions"),
		History: history,
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
