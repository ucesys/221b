package confluence_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/confluence/confluencetest"
)

var ctx = context.Background()

func noSleep(context.Context, time.Duration) error { return nil }

// Cloud (REST v2) and Data Center (REST v1) read and write pages alike:
// a space and its pages, every page of a listing, and a write refused
// when the page moved on since it was read.
func TestBothFlavorsReadAndWritePages(t *testing.T) {
	for _, flavor := range []string{confluence.Cloud, confluence.DataCenter} {
		t.Run(flavor, func(t *testing.T) {
			f := confluencetest.New("ann@example.com", "tok-123456")
			defer f.Close()
			c, _ := confluence.New(confluence.Config{BaseURL: f.URL, Flavor: flavor, Email: "ann@example.com", Token: "tok-123456", HTTP: f.Client(), Sleep: noSleep})
			home := f.AddSpace("INFRA")
			for _, title := range []string{"One", "Two", "Three"} {
				f.AddPage("INFRA", home, title, "<p>"+title+"</p>")
			}
			sp, err := c.Space(ctx, "INFRA")
			if err != nil || sp.HomepageID != home {
				t.Fatalf("space: %+v, %v", sp, err)
			}
			pages, err := c.Pages(ctx, sp, time.Time{})
			if err != nil || len(pages) != 4 {
				t.Fatalf("every page of the space, over two pages of the listing: %d, %v", len(pages), err)
			}
			kids, err := c.Children(ctx, home)
			if err != nil || len(kids) != 3 {
				t.Fatalf("children: %d, %v", len(kids), err)
			}
			pg, err := c.Create(ctx, sp, home, "Runbook", "<p>Restart it.</p>")
			if err != nil || pg.Version != 1 || pg.URL == "" {
				t.Fatalf("create: %+v, %v", pg, err)
			}
			got, err := c.Page(ctx, pg.ID)
			if err != nil || got.Body != "<p>Restart it.</p>" || got.ParentID != home {
				t.Fatalf("read: %+v, %v", got, err)
			}
			if _, err := c.Update(ctx, sp, pg.ID, 1, "", "Runbook", "<p>Restart it twice.</p>", "x"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Update(ctx, sp, pg.ID, 1, "", "Runbook", "<p>Stale.</p>", "x"); !confluence.VersionConflict(err) {
				t.Fatalf("a write over a version that moved on is refused: %v", err)
			}
			if _, err := c.Page(ctx, "999999"); !confluence.NotFound(err) {
				t.Fatalf("no such page: %v", err)
			}
		})
	}
}

// 429 and 5xx are tried again, after Retry-After; a wrong token is not.
func TestRateLimitsAreWaitedOut(t *testing.T) {
	f := confluencetest.New("ann@example.com", "tok-123456")
	defer f.Close()
	f.AddSpace("OPS")
	var waits []time.Duration
	c, _ := confluence.New(confluence.Config{BaseURL: f.URL, Flavor: confluence.DataCenter, Token: "tok-123456", HTTP: f.Client(), Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }})
	f.Throttle = 2
	if _, err := c.Space(ctx, "OPS"); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0] != time.Second {
		t.Fatalf("waited %v", waits)
	}
	bad, _ := confluence.New(confluence.Config{BaseURL: f.URL, Flavor: confluence.DataCenter, Token: "wrong", HTTP: f.Client(), Sleep: noSleep})
	if _, err := bad.Space(ctx, "OPS"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a wrong token: %v", err)
	}
	if _, err := confluence.New(confluence.Config{BaseURL: f.URL, Flavor: confluence.Cloud, Token: "x"}); err == nil {
		t.Fatal("Cloud needs the account's email")
	}
}

// The storage format reads as text, macros' parameters left out; the
// wiki's markdown writes as storage format.
func TestStorageFormat(t *testing.T) {
	text := confluence.Text(`<h1>Shop</h1><p>It runs on <strong>web</strong> &amp; db.</p><ac:structured-macro ac:name="code"><ac:parameter ac:name="language">bash</ac:parameter><ac:plain-text-body><![CDATA[kubectl get pods]]></ac:plain-text-body></ac:structured-macro><ul><li>one</li><li>two</li></ul><table><tbody><tr><th>Host</th><th>Address</th></tr><tr><td>db-3</td><td>10.0.0.3</td></tr></tbody></table>`)
	for _, want := range []string{"# Shop", "It runs on web & db.", "kubectl get pods", "- one", "db-3 | 10.0.0.3"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q is missing from:\n%s", want, text)
		}
	}
	if strings.Contains(text, "bash") {
		t.Errorf("a macro's parameter came through: %s", text)
	}
	md := confluence.Markdown("# Title\n\nSome **bold** `code` and a [link](https://example.com).\n\n- a\n- b\n\n```\nx < y\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |\n")
	for _, want := range []string{"<h1>Title</h1>", "<strong>bold</strong>", "<code>code</code>", `<a href="https://example.com">link</a>`, "<ul><li>a</li><li>b</li></ul>", "<![CDATA[x < y]]>", "<td>1</td>"} {
		if !strings.Contains(md, want) {
			t.Errorf("%q is missing from %s", want, md)
		}
	}
}
