package auth

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The findings of the review of the auth package (2026-11-23) that are
// best seen from inside it.

// slowSaves is a versioned backend with resource versions, as the Secret
// has, whose next save can be held and then fail as a timeout does.
type slowSaves struct {
	mu      sync.Mutex
	b       []byte
	rv      int
	hold    bool
	held    chan struct{}
	release chan struct{}
}

func (s *slowSaves) Load(ctx context.Context) ([]byte, error) {
	b, _, err := s.LoadVersion(ctx)
	return b, err
}

func (s *slowSaves) Save(context.Context, []byte) error { panic("not used") }

func (s *slowSaves) LoadVersion(context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := "rv:" + strconv.Itoa(100+s.rv)
	if s.b == nil {
		return nil, v, ErrNoAccounts
	}
	return append([]byte(nil), s.b...), v, nil
}

func (s *slowSaves) SaveIf(_ context.Context, b []byte, version string) error {
	s.mu.Lock()
	if s.hold {
		s.hold = false
		s.mu.Unlock()
		close(s.held)
		<-s.release
		return context.DeadlineExceeded
	}
	defer s.mu.Unlock()
	if version != "rv:"+strconv.Itoa(100+s.rv) {
		return ErrConflict
	}
	s.b, s.rv = append([]byte(nil), b...), s.rv+1
	return nil
}

// Finding 6: a save that fails with anything but a conflict while the
// accounts were read again meanwhile leaves the replica reading them
// again, not holding the old ones under the new version and writing them
// over a newer change.
func TestAFailedSaveDoesNotKeepOldAccountsUnderANewVersion(t *testing.T) {
	ctx := context.Background()
	backend := &slowSaves{}
	a := NewAccounts(backend, nil, nil, false, nil)
	other := NewAccounts(backend, nil, nil, false, nil)
	if _, err := a.Create(ctx, NewAccount{Username: "mallory", Password: "correct horse battery staple"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Load(ctx); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.hold, backend.held, backend.release = true, make(chan struct{}), make(chan struct{})
	backend.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.SignedIn("mallory", time.Now())
	}()
	<-backend.held
	disabled := true
	if _, _, err := other.Update(ctx, "mallory", Change{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	// a's reading every few seconds, while its save waits.
	if err := a.Load(ctx); err != nil {
		t.Fatal(err)
	}
	close(backend.release)
	<-done
	if err := a.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if u, _ := a.Find("mallory"); !u.Disabled {
		t.Fatal("after the failed save the replica holds mallory enabled")
	}
	a.SignedIn("mallory", time.Now())
	if err := other.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if u, _ := other.Find("mallory"); !u.Disabled {
		t.Fatal("the replica's next save enabled mallory again")
	}
}
