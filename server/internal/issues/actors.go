package issues

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/scrub"
)

// --- The owner's feedback on Cases (Phase 12, wave 6c, CF-35 to CF-37) ---
//
// Every change to a case records who made it: a person, an agent (the
// Auditor) or the system (the console on its own). A case has a type
// (security, outage, ...) and, when the people who work it give one, an
// incident number from another system ("INC0012345").

// Actor is who made a change: a person by their name, an agent by its
// name ("Auditor"), or the system (CF-35).
type Actor struct {
	Name string `json:"name"`
	// Kind is ActorPerson, ActorAgent or ActorSystem.
	Kind string `json:"kind"`
}

// The kinds of Actor.
const (
	ActorPerson = "person"
	ActorAgent  = "agent"
	ActorSystem = "system"
)

// SystemActor is the console when it changes a case on its own: a timer,
// a run's verification, a change applied.
var SystemActor = Actor{Name: "System", Kind: ActorSystem}

// ActorOf is a person as an actor: a system account (a token) is the
// system, under its own name.
func ActorOf(p Person) Actor {
	if p.System {
		if p.ID == systemPerson.ID {
			return SystemActor
		}
		return Actor{Name: p.Label(), Kind: ActorSystem}
	}
	return Actor{Name: firstNonEmpty(p.Label(), "someone"), Kind: ActorPerson}
}

// AgentActor is an agent as an actor, by its name.
func AgentActor(name string) Actor {
	return Actor{Name: firstNonEmpty(name, "an agent"), Kind: ActorAgent}
}

// UnmarshalJSON reads an actor, or a name alone as a person: what a case's
// history held before it named the kind.
func (a *Actor) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		*a = Actor{Name: name, Kind: ActorPerson}
		if name == "" || name == SystemActor.Name {
			a.Kind = ActorSystem
		}
		return nil
	}
	type plain Actor
	return json.Unmarshal(b, (*plain)(a))
}

// Label is the actor as a row says it: "Pat", "Auditor (agent)", "System".
func (a Actor) Label() string {
	if a.Kind == ActorAgent {
		return a.Name + " (agent)"
	}
	return a.Name
}

// The types of a case (CF-37).
const (
	TypeSecurity      = "security"
	TypeOutage        = "outage"
	TypePerformance   = "performance"
	TypeAvailability  = "availability"
	TypeConfiguration = "configuration"
	TypeCapacity      = "capacity"
	TypeCompliance    = "compliance"
	TypeQuestion      = "question"
	TypeOther         = "other"
)

// CaseTypes lists them in the order the console offers them.
var CaseTypes = []string{TypeSecurity, TypeOutage, TypePerformance, TypeAvailability, TypeConfiguration, TypeCapacity, TypeCompliance, TypeQuestion, TypeOther}

// ValidType says whether t is a type of case.
func ValidType(t string) bool { return slices.Contains(CaseTypes, t) }

// CaseType is the case's type: other when it has none.
func (r *Case) CaseType() string {
	if r.Type == "" {
		return TypeOther
	}
	return r.Type
}

// CaseType is the line's type: other when it has none.
func (l Line) CaseType() string {
	if l.Type == "" {
		return TypeOther
	}
	return l.Type
}

// checkType checks a type a person or an agent gave: "" is other.
func checkType(t string) (string, error) {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return TypeOther, nil
	}
	if !ValidType(t) {
		return "", invalid("a case's type is one of %s", strings.Join(CaseTypes, ", "))
	}
	return t, nil
}

// MaxIncident bounds an incident number, in characters.
const MaxIncident = 64

// CleanIncident checks an incident number (CF-36): 1 to 64 printable
// characters, "" for none.
func CleanIncident(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxIncident {
		return "", invalid("an incident number is 1 to %d printable characters", MaxIncident)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", invalid("an incident number is 1 to %d printable characters", MaxIncident)
		}
	}
	return scrub.Text(s), nil
}

// stamp names who changed a case and when (CF-35).
func stamp(r *Case, by Actor, at time.Time) {
	b := by
	r.LastBy, r.LastAt = &b, &at
}

// --- end of the owner's feedback.
