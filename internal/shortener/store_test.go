package shortener

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func openTemp(t *testing.T, clock *fakeClock) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "links.log")
	store, err := OpenStore(path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func TestCreateAssignsSequentialBase62IDs(t *testing.T) {
	store, _ := openTemp(t, newClock())
	first, created, err := store.Create("https://example.com/a", "", 0)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	second, _, _ := store.Create("https://example.com/b", "", 0)
	if first.ID != "1001" || second.ID != "1002" {
		t.Fatalf("ids = %q, %q", first.ID, second.ID)
	}
}

func TestCreateDeduplicatesPlainLinks(t *testing.T) {
	store, _ := openTemp(t, newClock())
	a, _, _ := store.Create("https://example.com/same", "", 0)
	b, created, _ := store.Create("https://example.com/same", "", 0)
	if created || a.ID != b.ID {
		t.Fatalf("expected the same link, got %q and %q (created=%v)", a.ID, b.ID, created)
	}
	if store.Len() != 1 {
		t.Fatalf("Len = %d", store.Len())
	}
}

func TestCustomAliases(t *testing.T) {
	store, _ := openTemp(t, newClock())
	link, _, err := store.Create("https://example.com/", "my-page", 0)
	if err != nil || link.ID != "my-page" || !link.Custom {
		t.Fatalf("alias create = %+v, %v", link, err)
	}
	if _, _, err := store.Create("https://example.org/", "my-page", 0); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("err = %v, want ErrAliasTaken", err)
	}
	for _, bad := range []string{"ab", "has space", "api", strings.Repeat("x", 33), "wat?"} {
		if _, _, err := store.Create("https://example.com/", bad, 0); !errors.Is(err, ErrInvalidAlias) {
			t.Errorf("alias %q: err = %v", bad, err)
		}
	}
}

func TestValidateURL(t *testing.T) {
	good := []string{"http://a.io", "https://example.com/path?q=1#frag"}
	bad := []string{"", "example.com", "ftp://example.com", "javascript:alert(1)", "http://", "https:///path", "http://" + strings.Repeat("a", MaxURLLength)}
	for _, raw := range good {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("%q should be valid: %v", raw, err)
		}
	}
	for _, raw := range bad {
		if err := ValidateURL(raw); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%q should be invalid, got %v", raw, err)
		}
	}
}

func TestNegativeTTLRejected(t *testing.T) {
	store, _ := openTemp(t, newClock())
	if _, _, err := store.Create("https://example.com/", "", -time.Second); !errors.Is(err, ErrNegativeTTL) {
		t.Fatalf("err = %v", err)
	}
}

func TestVisitCountsHits(t *testing.T) {
	store, _ := openTemp(t, newClock())
	link, _, _ := store.Create("https://example.com/", "", 0)
	for i := 0; i < 3; i++ {
		if _, err := store.Visit(link.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := store.Get(link.ID)
	if got.Hits != 3 {
		t.Fatalf("hits = %d", got.Hits)
	}
}

func TestExpiry(t *testing.T) {
	clock := newClock()
	store, _ := openTemp(t, clock)
	link, _, _ := store.Create("https://example.com/", "", time.Minute)
	if _, err := store.Visit(link.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if _, err := store.Visit(link.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("Visit err = %v", err)
	}
	if _, err := store.Get(link.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("Get err = %v", err)
	}
}

func TestExpiredAliasCanBeReused(t *testing.T) {
	clock := newClock()
	store, _ := openTemp(t, clock)
	store.Create("https://old.example/", "promo", time.Hour)
	clock.Advance(2 * time.Hour)
	link, _, err := store.Create("https://new.example/", "promo", 0)
	if err != nil || link.URL != "https://new.example/" {
		t.Fatalf("reuse = %+v, %v", link, err)
	}
}

func TestTemporaryLinksDoNotHijackDeduplication(t *testing.T) {
	store, _ := openTemp(t, newClock())
	permanent, _, _ := store.Create("https://example.com/x", "", 0)
	temp, _, _ := store.Create("https://example.com/x", "", time.Hour)
	if temp.ID == permanent.ID {
		t.Fatal("a ttl link must not reuse a permanent one")
	}
	again, created, _ := store.Create("https://example.com/x", "", 0)
	if created || again.ID != permanent.ID {
		t.Fatalf("dedupe broke: %q created=%v", again.ID, created)
	}
}

func TestDelete(t *testing.T) {
	store, _ := openTemp(t, newClock())
	link, _, _ := store.Create("https://example.com/", "", 0)
	if err := store.Delete(link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(link.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := store.Delete(link.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	again, created, _ := store.Create("https://example.com/", "", 0)
	if !created || again.ID == link.ID {
		t.Fatalf("a deleted id should not be reused, got %q", again.ID)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	clock := newClock()
	store, path := openTemp(t, clock)
	a, _, _ := store.Create("https://example.com/a", "", 0)
	store.Create("https://example.com/b", "named", 0)
	store.Visit(a.ID)
	store.Visit(a.ID)
	c, _, _ := store.Create("https://example.com/c", "", 0)
	store.Delete(c.ID)
	store.Close()

	reopened, err := OpenStore(path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(a.ID)
	if err != nil || got.URL != "https://example.com/a" || got.Hits != 2 {
		t.Fatalf("after reopen: %+v, %v", got, err)
	}
	if _, err := reopened.Get("named"); err != nil {
		t.Fatalf("alias lost: %v", err)
	}
	if _, err := reopened.Get(c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted link came back: %v", err)
	}
	next, _, _ := reopened.Create("https://example.com/d", "", 0)
	if next.ID != "1003" {
		t.Fatalf("next id = %q, want 1003", next.ID)
	}
}

func TestTruncatedTailIsRepaired(t *testing.T) {
	clock := newClock()
	store, path := openTemp(t, clock)
	store.Create("https://example.com/a", "", 0)
	store.Close()
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	file.WriteString(`{"op":"put","id":"zzz","url":"https://exa`)
	file.Close()

	reopened, err := OpenStore(path, clock.Now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reopened.Close()
	if reopened.Len() != 1 {
		t.Fatalf("Len = %d", reopened.Len())
	}
	if _, _, err := reopened.Create("https://example.com/b", "", 0); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptMiddleIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.log")
	os.WriteFile(path, []byte("not json\n{\"op\":\"del\",\"id\":\"x\"}\n"), 0o600)
	if _, err := OpenStore(path, nil); err == nil {
		t.Fatal("expected a corruption error")
	}
}

func TestCompactDropsExpiredAndKeepsCounters(t *testing.T) {
	clock := newClock()
	store, path := openTemp(t, clock)
	keep, _, _ := store.Create("https://example.com/keep", "", 0)
	store.Create("https://example.com/temp", "", time.Minute)
	store.Create("https://example.com/gone", "", 0)
	store.Delete("1003")
	store.Visit(keep.ID)
	clock.Advance(time.Hour)

	dropped, err := store.Compact()
	if err != nil || dropped != 1 {
		t.Fatalf("Compact = %d, %v", dropped, err)
	}
	if store.Len() != 1 {
		t.Fatalf("Len = %d", store.Len())
	}
	if next, _, _ := store.Create("https://example.com/new", "", 0); next.ID != "1004" {
		t.Fatalf("id after compact = %q, want 1004", next.ID)
	}
	store.Close()

	reopened, err := OpenStore(path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(keep.ID)
	if err != nil || got.Hits != 1 {
		t.Fatalf("kept link = %+v, %v", got, err)
	}
	if next, _, _ := reopened.Create("https://example.com/newer", "", 0); next.ID != "1005" {
		t.Fatalf("id after reopen = %q, want 1005", next.ID)
	}
}

func TestListOrdersNewestFirstAndPages(t *testing.T) {
	clock := newClock()
	store, _ := openTemp(t, clock)
	for _, name := range []string{"a", "b", "c", "d"} {
		store.Create("https://example.com/"+name, "", 0)
		clock.Advance(time.Second)
	}
	page := store.List(0, 2)
	if len(page) != 2 || page[0].URL != "https://example.com/d" || page[1].URL != "https://example.com/c" {
		t.Fatalf("page = %+v", page)
	}
	if rest := store.List(3, 10); len(rest) != 1 || rest[0].URL != "https://example.com/a" {
		t.Fatalf("rest = %+v", rest)
	}
	if empty := store.List(10, 10); len(empty) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestConcurrentCreates(t *testing.T) {
	store, _ := openTemp(t, newClock())
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				store.Create("https://example.com/"+Encode(uint64(w*100+i)), "", 0)
			}
		}(w)
	}
	wg.Wait()
	if store.Len() != 200 {
		t.Fatalf("Len = %d, want 200", store.Len())
	}
	seen := map[string]bool{}
	for _, link := range store.List(0, 0) {
		if seen[link.ID] {
			t.Fatalf("duplicate id %s", link.ID)
		}
		seen[link.ID] = true
	}
}

