package watson

import (
	"fmt"
	"strings"
)

// The console's own release (asked November 17, 2026, after an upgrade
// of it with an application's few values reset everything else to the
// chart's defaults and took the console down): a card that changes it
// says so first, and one that would also remove values it has now is
// not approved.

// SelfAlert is what a card that changes the console's own release says
// before anything else.
const SelfAlert = "This is the release that runs this console: if it fails, the console goes down."

// Card tones: a card that removes values warns; one that changes the
// console's own release is a danger.
const (
	ToneWarn   = "warn"
	ToneDanger = "danger"
)

// cardAlert works out a helm card's tone, its prominent alert and why it
// may not be approved, from the command and what the plan found: the
// values an upgrade would remove from the release.
func (s *Service) cardAlert(cluster string, c *Command, pin *appPin) (tone, alert, refusal string) {
	removed := 0
	if pin != nil {
		removed = pin.removed
	}
	if removed > 0 {
		tone = ToneWarn
	}
	if c == nil || len(c.Positionals) == 0 {
		return tone, "", ""
	}
	switch c.Name() {
	case "helm install", "helm upgrade", "helm rollback", "helm uninstall":
	default:
		return tone, "", ""
	}
	if !s.opts.Self.Is(cluster, helmNamespace(c), c.Positionals[0]) {
		return tone, "", ""
	}
	tone, alert = ToneDanger, SelfAlert
	if removed > 0 {
		refusal = fmt.Sprintf("Refused: this upgrade removes %s from the release that runs this console; they would fall back to the chart's defaults, which can take the console down. Put them in the application's values first (this card lists them), then send the command again.", plural(removed, "value", "values"))
	}
	return tone, alert, refusal
}

// valueKeys is how many keys a card names before "and N more".
const valueKeys = 20

// valueImpact says on a deploy card which of the release's values the
// deploy removes and which it changes: by key, never by value.
func valueImpact(removed, changed []string) []string {
	var out []string
	if len(removed) > 0 {
		out = append(out, fmt.Sprintf("Removes %s the release has now (helm get values): %s. They fall back to the chart's defaults.", plural(len(removed), "value", "values"), listText(removed, valueKeys)))
	}
	if len(changed) > 0 {
		out = append(out, fmt.Sprintf("Changes %s the release has now: %s.", plural(len(changed), "value", "values"), listText(changed, valueKeys)))
	}
	return out
}

// withAlert puts the alert first among a card's impact lines, for the
// cards that show only those.
func withAlert(alert string, impact []string) []string {
	if alert == "" || (len(impact) > 0 && strings.HasPrefix(impact[0], alert)) {
		return impact
	}
	return append([]string{alert}, impact...)
}
