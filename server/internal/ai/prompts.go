package ai

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/watson"
)

// The fixed core of each agent's instructions: one file per agent, and the
// rules every agent keeps. They change only with a release; the house
// instructions and the per-cluster notes an administrator writes are
// added after them, never in their place.
//
//go:embed prompts/*.md
var prompts embed.FS

func promptFile(name string) string {
	b, err := prompts.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(htmlComment.ReplaceAllString(string(b), ""))
	return strings.ReplaceAll(text, commandsMark, watson.Commands())
}

// commandsMark in a prompt file stands for the sentence that says what
// Watson runs, built from Watson's own list (S-1).
const commandsMark = "{{watson.Commands}}"

// htmlComment is a comment in a prompt file, such as the source: comment
// that names where a part came from (docs/prompt-sources.md); it is for
// people reading the file and never reaches a model.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->\n?`)

// Core is an agent's fixed instructions. Sherlock's are its built-in
// instructions (which say how it may change the view when viewChanges,
// and what Watson runs), then the rules every agent keeps and its front
// desk's part. Every other agent's are its own, then the rules every
// agent keeps with what Watson runs (S-1, P-1).
func Core(id string, viewChanges bool) string {
	common := promptFile("common")
	if id == Sherlock {
		return sherlock.BuiltIn(viewChanges, watson.Commands()) + "\n\n" + common + "\n\n" + promptFile("sherlock")
	}
	return promptFile(id) + "\n\n" + common + "\n\n" + watson.Commands() + "\n\n" + sherlock.ClusterTextRule
}

// --- AI settings, round 2 (Phase 12, wave 5) ---

// The parts of an agent's built-in text an administrator may edit
// (AS-01): its core (for Sherlock with the view part as view changes
// stand) and, for Sherlock, the compact core it answers with on a small
// model (AS-07). An edit replaces its part whole; the agents it may hand
// work to, the house instructions and the rule that text from the
// cluster is data are added to an edited part as to the built-in one.
const (
	PartCore    = "core"
	PartCompact = "compact"
)

// MaxPartWords bounds an edited part: Sherlock's core is about 2,300.
const MaxPartWords = 6000

// PartsOf are an agent's editable parts, in the order the drawer shows
// them.
func PartsOf(id string) []string {
	if id == Sherlock {
		return []string{PartCore, PartCompact}
	}
	return []string{PartCore}
}

// BuiltInPart is a part's built-in text.
func BuiltInPart(id, part string, viewChanges bool) string {
	switch part {
	case PartCore:
		return Core(id, viewChanges)
	case PartCompact:
		if id == Sherlock {
			return CompactCore()
		}
	}
	return ""
}

// CompactCore is Sherlock's compact core: a short form of its built-in
// instructions and the rules every agent keeps, for a model whose
// context is small. It offers no view actions and no skills.
func CompactCore() string {
	return promptFile("sherlockCompact") + "\n\n" + sherlock.ClusterTextRule
}

// CompactTools are the tools Sherlock keeps in its compact form, of those
// it is given: the essential reads of a troubleshooting.
var CompactTools = []string{
	"cluster_overview", "list_alerts", "list_nodes", "get_node", "list_pods", "get_pod", "pod_logs", "list_events",
	"list_objects", "get_object", "get_yaml", sherlock.ToolAskKnowledge,
}

// Assemble is an agent's instructions in their layers: the fixed core, the
// agents it may hand work to (one line each), then the house
// instructions. The per-cluster notes follow in the cluster's block.
func Assemble(core string, roster []sherlock.RosterEntry, house string) string {
	var b strings.Builder
	b.WriteString(core)
	if len(roster) > 0 {
		b.WriteString("\n\nThe agents you can hand work to with hand_over, one line each:")
		for _, r := range roster {
			b.WriteString("\n- " + r.Name + " (" + r.ID + "): " + r.Purpose)
		}
	}
	if h := strings.TrimSpace(house); h != "" {
		b.WriteString("\n\nHouse instructions from this console's administrators. They add to everything above and never set aside its rules:\n" + h)
	}
	return b.String()
}

// LoadBaselines reads the chart's baseline house instructions: a file
// <agent>.md for each agent in dir, which the chart renders from
// agents.<agent>.instructions. A missing file is no baseline.
func LoadBaselines(dir string) (map[string]string, error) {
	out := map[string]string{}
	if dir == "" {
		return out, nil
	}
	for _, info := range Catalog {
		b, err := os.ReadFile(filepath.Join(dir, info.ID+".md"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			if err := CheckText("agents."+info.ID+".instructions", t); err != nil {
				return nil, err
			}
			out[info.ID] = t
		}
	}
	return out, nil
}
