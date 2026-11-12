package api

import (
	"encoding/json"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
)

// backupFile is a backup with the console's own parts typed, as the tests
// read and write one: the keys are the parts' names (ADR-80).
type backupFile struct {
	Format          string             `json:"format"`
	Version         string             `json:"version"`
	Taken           time.Time          `json:"taken"`
	By              string             `json:"by,omitempty"`
	LeftOut         []string           `json:"leftOut"`
	Secrets         *backup.Sealing    `json:"secrets,omitempty"`
	Settings        *settings.Settings `json:"settings,omitempty"`
	Users           []backupUser       `json:"users,omitempty"`
	Connections     []backupConnection `json:"connections,omitempty"`
	Apps            []backupApp        `json:"apps,omitempty"`
	SherlockKey     string             `json:"sherlockKey,omitempty"`
	SSHAuthority    string             `json:"sshAuthority,omitempty"`
	ToolboxSessions []sessions.Record  `json:"toolboxSessions,omitempty"`
	SherlockSpend   json.RawMessage    `json:"sherlockSpend,omitempty"`
	SSHPairings     json.RawMessage    `json:"sshPairings,omitempty"`
	SSHHosts        json.RawMessage    `json:"sshHosts,omitempty"`
	Audit           []audit.Event      `json:"audit,omitempty"`
	Automation      *backupAutomation  `json:"automation,omitempty"`
}

// restoreInput is a restore's request with the file typed.
type restoreInput struct {
	Backup backupFile      `json:"backup"`
	Parts  map[string]bool `json:"parts"`
	Source string          `json:"source,omitempty"`
	// WithoutSecrets is the yes to a restore the key cannot open (ADR-137).
	WithoutSecrets bool `json:"withoutSecrets,omitempty"`
}
