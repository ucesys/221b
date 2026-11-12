package confluence

import (
	"context"

	"github.com/ucesys/221b/server/internal/issues"
)

// IssueSource is the issue service as the publisher reads it: every known
// issue as an operator sees them, its services and settings; a page
// imported as a draft known issue from Confluence; and a person's edit
// kept as an update.
func IssueSource(s *issues.Service) KnownIssues { return issueSource{s} }

type issueSource struct{ s *issues.Service }

// operator is how the publisher reads: as the system, seeing drafts too,
// which it never publishes.
var operator = issues.Caller{Person: issues.Person{ID: "system:confluence", Name: "Confluence publisher", System: true}, Operator: true}

func (i issueSource) All(ctx context.Context) ([]issues.KnownIssue, error) {
	return i.s.Known(ctx, operator, true)
}

func (i issueSource) Services(ctx context.Context) ([]issues.ServiceDef, error) {
	return i.s.Services(ctx)
}

func (i issueSource) Settings(ctx context.Context) (issues.Settings, error) {
	return i.s.Settings(ctx)
}

func (i issueSource) Import(ctx context.Context, in issues.KnownInput) (issues.KnownIssue, error) {
	return i.s.Propose(ctx, issues.SourceConfluence, in)
}

func (i issueSource) Note(ctx context.Context, id, text string, by Person) error {
	_, err := i.s.AddUpdate(ctx, issues.Caller{Person: issues.Person{ID: by.ID, Name: by.Name, Email: by.Email}, Operator: true}, id, text, "")
	return err
}
