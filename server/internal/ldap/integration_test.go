package ldap

import (
	"context"
	"os"
	"testing"
	"time"
)

// A test against a real directory, for CI: start OpenLDAP with a user and a
// group, and set LDAP_TEST_URL, LDAP_TEST_BIND_DN, LDAP_TEST_BIND_PASSWORD,
// LDAP_TEST_USER_BASE, LDAP_TEST_GROUP_BASE, LDAP_TEST_USER and
// LDAP_TEST_PASSWORD. It checks what the fake server above cannot: that the
// encoding is what a real server accepts (RB-19, RB-56).
func TestRealDirectory(t *testing.T) {
	url := os.Getenv("LDAP_TEST_URL")
	if url == "" {
		t.Skip("LDAP_TEST_URL is not set")
	}
	d, err := New(Options{
		Config: Config{URL: url, StartTLS: os.Getenv("LDAP_TEST_STARTTLS") != "", Timeout: 10 * time.Second},
		BindDN: os.Getenv("LDAP_TEST_BIND_DN"), BindPassword: os.Getenv("LDAP_TEST_BIND_PASSWORD"),
		UserBase: os.Getenv("LDAP_TEST_USER_BASE"), GroupBase: os.Getenv("LDAP_TEST_GROUP_BASE"),
		Nested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := d.Authenticate(ctx, os.Getenv("LDAP_TEST_USER"), os.Getenv("LDAP_TEST_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("signed in %s with groups %v", p.Username, p.Groups)
	if len(p.Groups) == 0 {
		t.Error("the test user should be in a group")
	}
	if _, err := d.Authenticate(ctx, os.Getenv("LDAP_TEST_USER"), "definitely-wrong"); err == nil {
		t.Error("a wrong password signed in")
	}
}
