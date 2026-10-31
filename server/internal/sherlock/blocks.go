package sherlock

import (
	"regexp"
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

// fence is a code fence as the page reads it (web/src/lib/markdown.tsx):
// backticks or tildes, any fence closes a block, and a block with none
// runs to the end of the answer.
var fence = regexp.MustCompile("^\\s*(```|~~~)\\s*([\\w+-]*)\\s*$")

// FixBlocks rewrites the answer's shell blocks: commands that start with
// kubectl or helm and that check refuses leave the block for a text block
// under it, each with why. A command is what Watson reads: lines joined
// by a trailing backslash go and stay together. Other blocks are
// untouched; nil check changes nothing.
func FixBlocks(text string, check func(line string) error) string {
	if check == nil {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		m := fence.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && !fence.MatchString(lines[end]) {
			end++
		}
		closed := end < len(lines)
		last := end
		if !closed {
			last = len(lines) - 1
		}
		var ok, refused []string
		if shellFence(m[2]) {
			ok, refused = sortCommands(lines[i+1:end], check)
		}
		if len(refused) == 0 {
			out = append(out, lines[i:last+1]...)
			i = last
			continue
		}
		if hasCommand(ok) {
			out = append(out, lines[i])
			out = append(out, ok...)
			out = append(out, "```", "")
		}
		out = append(out, "Watson cannot run these; run them yourself:", "", "```text")
		out = append(out, refused...)
		out = append(out, "```")
		i = last
	}
	return strings.Join(out, "\n")
}

// sortCommands splits a shell block's lines into those that stay and the
// refused commands, each after a comment with the reason.
func sortCommands(body []string, check func(line string) error) (ok, refused []string) {
	for i := 0; i < len(body); i++ {
		end := i
		joined := strings.TrimSpace(body[i])
		for strings.HasSuffix(joined, `\`) && end+1 < len(body) {
			end++
			joined = strings.TrimSpace(strings.TrimSuffix(joined, `\`)) + " " + strings.TrimSpace(body[end])
		}
		command := body[i : end+1]
		i = end
		if !strings.HasPrefix(joined, "kubectl ") && !strings.HasPrefix(joined, "helm ") {
			ok = append(ok, command...)
			continue
		}
		if err := check(joined); err != nil {
			refused = append(refused, "# "+strings.TrimSpace(err.Error()))
			refused = append(refused, command...)
			continue
		}
		ok = append(ok, command...)
	}
	return ok, refused
}

func hasCommand(lines []string) bool {
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}
