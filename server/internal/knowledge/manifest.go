package knowledge

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Manifest is what was read of every source item: its version and hash,
// when it was read and last seen, the pages that draw on it and the hosts
// it lists. An item whose version and hash did not change is not read or
// sent to a model again. It is kept beside the pages, as manifest.json.
type Manifest struct {
	Items   map[string]*ItemState `json:"items"`
	Updated time.Time             `json:"updated,omitzero"`
	// Retired are the pages people retired (KN-24), by path: out of every
	// replica's index, never rewritten by Mycroft, their files kept in the
	// store. The manifest holds them because every replica reads it with
	// the pages, and it travels with the wiki to another store and into a
	// backup.
	Retired map[string]Retirement `json:"retired,omitempty"`
}

// Retirement is why a page was retired, who asked, who approved, and
// when.
type Retirement struct {
	Reason     string    `json:"reason"`
	AskedBy    string    `json:"askedBy"`
	AskedAt    time.Time `json:"askedAt,omitzero"`
	ApprovedBy string    `json:"approvedBy"`
	At         time.Time `json:"at"`
	// Request is the request approved.
	Request string `json:"request,omitempty"`
}

// ItemState is one source item.
type ItemState struct {
	Source  string `json:"source"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version string `json:"version,omitempty"`
	// Hash is the scrubbed text's.
	Hash string `json:"hash,omitempty"`
	URL  string `json:"url,omitempty"`
	// Modified is the source's own date for the item.
	Modified time.Time `json:"modified,omitzero"`
	LastRead time.Time `json:"lastRead,omitzero"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
	// Gone says a full read did not list it any more.
	Gone bool `json:"gone,omitempty"`
	// Pages are the wiki pages that draw on it.
	Pages []string `json:"pages,omitempty"`
	// Hosts are the hosts it lists, as Mycroft read them, and Fleets the
	// groups of them it names (Phase 12, GS-11).
	Hosts  []HostFact  `json:"hosts,omitempty"`
	Fleets []FleetFact `json:"fleets,omitempty"`
	// Reported is what in it tried to give instructions, when something
	// did.
	Reported string `json:"reported,omitempty"`
	// Corrects is the page a correction was written for (This is
	// outdated…, KN-23), and By who added the item, for what people add
	// in Mycroft.
	Corrects string `json:"corrects,omitempty"`
	By       string `json:"by,omitempty"`
}

// HostFact is a host a document lists, with the labels it gives it.
type HostFact struct {
	Name    string            `json:"name"`
	Address string            `json:"address,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// FleetFact is a group of hosts a document names (web servers, backup
// servers), with the labels its hosts share.
type FleetFact struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

func newManifest() *Manifest { return &Manifest{Items: map[string]*ItemState{}} }

func itemKey(source, id string) string { return source + ":" + id }

func parseManifest(b []byte) *Manifest {
	m := newManifest()
	if len(b) > 0 {
		_ = json.Unmarshal(b, m)
	}
	if m.Items == nil {
		m.Items = map[string]*ItemState{}
	}
	return m
}

func (m *Manifest) clone() *Manifest {
	b, _ := json.Marshal(m)
	return parseManifest(b)
}

func (m *Manifest) encode() []byte {
	b, _ := json.MarshalIndent(m, "", " ")
	return b
}

// ref is how a page names an item.
func (st *ItemState) ref() SourceRef {
	date := st.Modified
	if date.IsZero() {
		date = st.LastRead
	}
	return SourceRef{Key: itemKey(st.Source, st.ID), Title: st.Title, Version: st.Version, Date: date.UTC(), URL: st.URL}
}

// bySource are a source's items, by key.
func (m *Manifest) bySource(source string) []*ItemState {
	var out []*ItemState
	for _, st := range m.Items {
		if st.Source == source {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// lastSeen is when any of a page's sources was last seen.
func (m *Manifest) lastSeen(refs []SourceRef) (time.Time, bool) {
	var last time.Time
	found := false
	for _, r := range refs {
		if st := m.Items[r.Key]; st != nil {
			found = true
			if st.LastSeen.After(last) {
				last = st.LastSeen
			}
		}
	}
	return last, found
}

// Excerpt is the scrubbed text of one source item, cut into chunks, kept
// for the index: what an agent quotes when no page covers a question.
type Excerpt struct {
	Key     string    `json:"key"`
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Version string    `json:"version,omitempty"`
	Date    time.Time `json:"date,omitzero"`
	URL     string    `json:"url,omitempty"`
	Chunks  []string  `json:"chunks"`
}

// Excerpt limits: a chunk is about a paragraph or a few.
const (
	chunkSize = 1200
	maxChunks = 24
)

func excerptPath(key string) string {
	source, _, _ := strings.Cut(key, ":")
	return ExcerptsDir + source + "/" + hashOf([]byte(key))[:20] + ".json"
}

// chunks cuts text at paragraph ends into pieces of about chunkSize.
func chunks(text string) []string {
	var out []string
	var cur strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for len(para) > chunkSize {
			// After a sentence or a word where there is one; on a
			// character's boundary where there is none.
			cut := strings.LastIndexAny(para[:chunkSize], ".\n ") + 1
			if cut < chunkSize/2 {
				cut = chunkSize
				for cut > 0 && (para[cut]&0xC0) == 0x80 {
					cut--
				}
			}
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			out = append(out, strings.TrimSpace(para[:cut]))
			para = strings.TrimSpace(para[cut:])
		}
		if cur.Len()+len(para) > chunkSize && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
		if len(out) >= maxChunks {
			break
		}
	}
	if cur.Len() > 0 && len(out) < maxChunks {
		out = append(out, cur.String())
	}
	if len(out) > maxChunks {
		out = out[:maxChunks]
	}
	return out
}
