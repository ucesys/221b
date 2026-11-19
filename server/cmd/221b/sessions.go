package main

import (
	"context"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// The toolbox's sessions (Phase 9).

// wireSessions starts the toolbox's sessions.
func (w *wiring) wireSessions(ctx context.Context) {
	log := w.log
	// The ended sessions' records: in the database when there is one (Phase
	// 13), with what the ConfigMap held copied over once.
	kept := &sessions.KVHistory{Backend: w.spendBackend, Key: sessions.HistoryKey, Keep: w.toolboxCfg.History.Keep, MaxBytes: historyMaxBytes, Logger: log.With("component", "sessions")}
	var history sessions.Archive = kept
	if w.database != nil {
		pg := &sessions.PGHistory{DB: w.database, Keep: w.toolboxCfg.History.Keep, Logger: log.With("component", "sessions")}
		if _, inMemory := w.spendBackend.(*kv.Memory); !inMemory {
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
		history = pg
	}
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
