package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// Record is what is kept of an ended session for Administration ›
// Sessions (TB-44, TB-45): no output, no transcript, the facts.
type Record struct {
	ID        string    `json:"id"`
	Cluster   string    `json:"cluster"`
	Tool      string    `json:"tool"`
	Summary   string    `json:"summary"`
	Namespace string    `json:"namespace,omitempty"`
	Pod       string    `json:"pod,omitempty"`
	Container string    `json:"container,omitempty"`
	Node      string    `json:"node,omitempty"`
	Owner     string    `json:"owner"`
	OwnerUser string    `json:"ownerUser,omitempty"`
	Approver  string    `json:"approver,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Created   time.Time `json:"created"`
	Started   time.Time `json:"started,omitempty"`
	Ended     time.Time `json:"ended"`
	EndedBy   string    `json:"endedBy"`
	EndNote   string    `json:"endNote,omitempty"`
	Exit      *int      `json:"exit,omitempty"`
	Recorded  bool      `json:"recorded,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	Conns     int       `json:"connections,omitempty"`
	Watchers  []string  `json:"watchers,omitempty"`
}

func recordOf(s *Session) Record {
	r := Record{ID: s.ID, Cluster: s.Cluster, Tool: s.Req.Tool, Summary: s.Req.Summary(), Namespace: s.Req.Namespace, Pod: s.Req.Pod, Container: s.Container, Node: s.Req.Node,
		Owner: s.Owner.Name, OwnerUser: s.Owner.User, Approver: s.Approver, Reason: s.Req.Reason, Created: s.Created, Started: s.Started, Ended: s.Ended,
		EndedBy: s.EndedBy, EndNote: s.EndNote, Exit: s.Exit, Recorded: s.Recorded, Conns: s.Forward.Total, Watchers: append([]string(nil), s.Watchers...)}
	if s.Req.Tool == "forward" {
		r.Bytes = s.Forward.BytesIn + s.Forward.BytesOut
	} else {
		r.Bytes = int64(len(s.ring.buf))
	}
	return r
}

// HistoryKey is the key of the settings ConfigMap the records are kept
// under.
const HistoryKey = "toolbox-sessions.json"

// History keeps the records of ended sessions.
type History interface {
	Add(Record)
	// List returns the records, newest first.
	List() []Record
}

// MemoryHistory keeps records until a restart.
type MemoryHistory struct {
	Keep int
	mu   sync.Mutex
	list []Record
}

func (h *MemoryHistory) Add(r Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.list = append([]Record{r}, h.list...)
	if h.Keep > 0 && len(h.list) > h.Keep {
		h.list = h.list[:h.Keep]
	}
}

func (h *MemoryHistory) List() []Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Record(nil), h.list...)
}

// KVHistory keeps the records under one key of the settings ConfigMap
// (TB-45), so every replica and a restarted console see them. Records are
// small and capped; the audit log stays the full record.
type KVHistory struct {
	Backend kv.Backend
	Key     string
	Keep    int
	Logger  *slog.Logger
	mu      sync.Mutex
	cache   []Record
	loaded  bool
}

func (h *KVHistory) load(ctx context.Context) ([]Record, string, error) {
	b, version, err := h.Backend.LoadKey(ctx, h.Key)
	if errors.Is(err, kv.ErrNotFound) {
		return nil, version, nil
	}
	if err != nil {
		return nil, "", err
	}
	var list []Record
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, version, nil
	}
	return list, version, nil
}

func (h *KVHistory) Add(r Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for try := 0; try < 5; try++ {
		list, version, err := h.load(ctx)
		if err != nil {
			break
		}
		list = append([]Record{r}, list...)
		if h.Keep > 0 && len(list) > h.Keep {
			list = list[:h.Keep]
		}
		b, _ := json.Marshal(list)
		if _, err := h.Backend.SaveKey(ctx, h.Key, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		} else if err != nil {
			break
		}
		h.cache, h.loaded = list, true
		return
	}
	if h.Logger != nil {
		h.Logger.Warn("the session record was not saved; the audit log has it", "session", r.ID)
	}
	h.cache = append([]Record{r}, h.cache...)
}

func (h *KVHistory) List() []Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if list, _, err := h.load(ctx); err == nil {
		h.cache, h.loaded = list, true
	}
	return append([]Record(nil), h.cache...)
}

// ---------- transcripts ----------

// transcript writes a terminal session as asciicast v2 (TB-54): a header,
// then one line per output, input or resize, with the seconds since the
// start.
type transcript struct {
	mu    sync.Mutex
	f     *os.File
	start time.Time
	enc   *json.Encoder
}

// TranscriptPath is where a session's transcript is kept.
func TranscriptPath(dir, cluster, id string) string {
	return filepath.Join(dir, filepath.Base(cluster), filepath.Base(id)+".cast")
}

func newTranscript(dir string, s *Session, now time.Time) (*transcript, error) {
	p := TranscriptPath(dir, s.Cluster, s.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	t := &transcript{f: f, start: now, enc: json.NewEncoder(f)}
	header := map[string]any{"version": 2, "width": 120, "height": 30, "timestamp": now.Unix(), "title": s.Req.Summary(),
		"env": map[string]string{"SESSION": s.ID, "USER": s.Owner.Name, "APPROVER": s.Approver}}
	if err := t.enc.Encode(header); err != nil {
		f.Close()
		return nil, err
	}
	return t, nil
}

func (t *transcript) event(kind string, b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return
	}
	_ = t.enc.Encode([]any{time.Since(t.start).Seconds(), kind, string(b)})
}

func (t *transcript) resize(sz Size) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return
	}
	_ = t.enc.Encode([]any{time.Since(t.start).Seconds(), "r", jsonSize(sz)})
}

// jsonSize is asciicast's resize data: COLSxROWS.
func jsonSize(sz Size) string { return fmt.Sprintf("%dx%d", sz.Cols, sz.Rows) }

func (t *transcript) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}

// PruneTranscripts removes transcripts older than the retention.
func PruneTranscripts(dir string, keep time.Duration, now time.Time) {
	if dir == "" || keep <= 0 {
		return
	}
	var old []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".cast" {
			return nil
		}
		if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) > keep {
			old = append(old, p)
		}
		return nil
	})
	sort.Strings(old)
	for _, p := range old {
		_ = os.Remove(p)
	}
}
