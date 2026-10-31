package sherlock

import (
	"strings"
)

// Watson runs what a fenced shell block of an answer offers with its
// Send to Watson button (P6-06); a line it would refuse, such as one
// with a pipe, is kept out of that block (asked October 30, 2026):
// FixBlocks moves such lines into a text block of their own, with the
// reason, for the person to run.

// shellFence says whether a fence's language is one the page treats as
// commands for Watson: sh, bash, shell, console, zsh, or none.
func shellFence(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "sh", "bash", "shell", "console", "zsh":
		return true
	}
	return false
}

// FixBlocks rewrites the answer's shell blocks: lines that start with
// kubectl or helm and that check refuses leave the block for a text
// block under it, each with why. Other blocks are untouched; nil check
// changes nothing.
func FixBlocks(text string, check func(line string) error) string {
	if check == nil || !strings.Contains(text, "```") {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") {
			out = append(out, line)
			continue
		}
		lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		// Find the closing fence.
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "```" {
				end = j
				break
			}
		}
		if end < 0 || !shellFence(lang) {
			out = append(out, line)
			continue
		}
		body := lines[i+1 : end]
		var ok, refused []string
		for _, l := range body {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") || !(strings.HasPrefix(t, "kubectl ") || strings.HasPrefix(t, "helm ")) {
				ok = append(ok, l)
				continue
			}
			if err := check(t); err != nil {
				refused = append(refused, "# "+strings.TrimSpace(err.Error()), t)
				continue
			}
			ok = append(ok, l)
		}
		if len(refused) == 0 {
			out = append(out, lines[i:end+1]...)
			i = end
			continue
		}
		if hasCommand(ok) {
			out = append(out, line)
			out = append(out, ok...)
			out = append(out, "```")
			out = append(out, "")
		}
		out = append(out, "Watson cannot run these; run them yourself:", "", "```text")
		out = append(out, refused...)
		out = append(out, "```")
		i = end
	}
	return strings.Join(out, "\n")
}

func hasCommand(lines []string) bool {
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}
