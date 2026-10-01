package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWorker is the Worker's ingest in process: it keeps events by id, as D1
// does, and can be made to fail.
type fakeWorker struct {
	mu       sync.Mutex
	events   map[string]Event
	requests int
	status   int // answer this instead of storing, when non-zero
	storeAnd int // store, then answer this (a timeout after the Worker stored)
	hang     time.Duration
	drop     bool // hang up without answering
	token    string
}

func newFake() (*fakeWorker, *httptest.Server) {
	f := &fakeWorker{events: map[string]Event{}, token: "tok"}
	return f, httptest.NewServer(f)
}

func (f *fakeWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	status, storeAnd, hang, drop := f.status, f.storeAnd, f.hang, f.drop
	f.mu.Unlock()
	if drop {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	if hang > 0 {
		// Read the body first: the server notices a client hanging up only
		// once the body is consumed.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(hang):
		case <-r.Context().Done():
			return
		}
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/ledger/events" || r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	if status != 0 {
		http.Error(w, "down", status)
		return
	}
	var in struct{ Events []Event }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Events) > maxBatch {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	for _, e := range in.Events {
		f.events[e.ID] = e
	}
	f.mu.Unlock()
	if storeAnd != 0 {
		http.Error(w, "stored, but answered badly", storeAnd)
		return
	}
	_, _ = w.Write([]byte(`{"accepted":1}`))
}

func (f *fakeWorker) count() (events, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events), f.requests
}

func (f *fakeWorker) set(fn func(*fakeWorker)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClient(t *testing.T, url string, clk *clock) *Client {
	t.Helper()
	c := New(Config{URL: url, Token: "tok", Dir: t.TempDir(), Version: "test", Now: clk.now})
	if c == nil {
		t.Fatal("New returned nil with a URL and a token")
	}
	return c
}

func spooled(t *testing.T, c *Client) int {
	t.Helper()
	n := 0
	for _, name := range []string{"spool.jsonl", "inflight.jsonl"} {
		b, err := os.ReadFile(filepath.Join(c.cfg.Dir, name))
		if err == nil {
			n += strings.Count(string(b), "\n")
		}
	}
	return n
}

func TestOffWhenUnset(t *testing.T) {
	t.Setenv("IRGO_LEDGER_URL", "")
	t.Setenv("IRGO_LEDGER_TOKEN", "x")
	if c := FromEnv(t.TempDir(), "v"); c != nil {
		t.Fatal("FromEnv with no URL returned a Client")
	}
	t.Setenv("IRGO_LEDGER_URL", "http://x")
	t.Setenv("IRGO_LEDGER_TOKEN", "")
	if c := FromEnv(t.TempDir(), "v"); c != nil {
		t.Fatal("FromEnv with no token returned a Client")
	}
	// A nil Client, and Emit before Configure, do nothing and do not panic.
	var c *Client
	if err := c.Record(Event{Type: Start}); err != nil {
		t.Fatal(err)
	}
	c.FlushAsync()
	c.Drain(time.Second)
	Configure(nil)
	Emit(Event{Type: Start})
	DrainDefault(time.Second)
}

func TestRecordFillsAndRedacts(t *testing.T) {
	f, srv := newFake()
	defer srv.Close()
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	c := newClient(t, srv.URL, clk)
	home, _ := os.UserHomeDir()
	exit := int64(1)
	if err := c.Record(Event{Type: End, Op: "op-123456", VM: "w1", Command: "app-create", Exit: &exit,
		Detail: "failed at " + home + "/secret/x: token=abc123 via https://u:p@host/x key " + strings.Repeat("a", 40)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.count(); n != 1 {
		t.Fatalf("%d events arrived, want 1", n)
	}
	var e Event
	for _, e = range f.events {
	}
	if len(e.ID) != 32 || e.TS != clk.now().UnixMilli() || !isMachineID(e.Machine) || e.Version != "test" || e.Client != "cli" || e.Exit == nil || *e.Exit != 1 {
		t.Errorf("not filled in: %+v", e)
	}
	for _, leak := range []string{home, "abc123", "u:p@", strings.Repeat("a", 40)} {
		if strings.Contains(e.Detail, leak) {
			t.Errorf("detail %q still holds %q", e.Detail, leak)
		}
	}
	if !strings.Contains(e.Detail, "~/secret/x") {
		t.Errorf("detail %q lost the shape of the path", e.Detail)
	}
	// The machine id is stable across Clients on the same directory.
	c2 := New(Config{URL: srv.URL, Token: "tok", Dir: c.cfg.Dir})
	if c2.id.machine() != e.Machine {
		t.Errorf("machine id changed: %s then %s", e.Machine, c2.id.machine())
	}
	if spooled(t, c) != 0 {
		t.Error("the spool is not empty after a successful flush")
	}
}

// Negative control (by hand, 1 Oct 2026): making flushRound remove
// inflight.jsonl when post fails loses the events, and this fails with
// "0 events spooled after a failed flush, want 3" (and two more tests fail); restored.
func TestOfflineSpoolsThenFlushes(t *testing.T) {
	f, srv := newFake()
	defer srv.Close()
	f.set(func(f *fakeWorker) { f.drop = true }) // connections die: the network is down
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	c := newClient(t, srv.URL, clk)
	for i := range 3 {
		if err := c.Record(Event{Type: Start, Op: fmt.Sprintf("op-%08d", i), Command: "doctor"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("a flush over a dead network reported success")
	}
	if n := spooled(t, c); n != 3 {
		t.Fatalf("%d events spooled after a failed flush, want 3", n)
	}
	// The network is back, but within the backoff nothing is even tried.
	f.set(func(f *fakeWorker) { f.drop, f.requests = false, 0 })
	if err := c.Flush(context.Background()); err != errOffline {
		t.Fatalf("flush during backoff: %v, want errOffline", err)
	}
	if _, reqs := f.count(); reqs != 0 {
		t.Fatalf("%d requests during backoff, want 0", reqs)
	}
	// More happens meanwhile, then the backoff passes.
	_ = c.Record(Event{Type: End, Op: "op-00000000", Command: "doctor"})
	clk.add(2 * time.Minute)
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.count(); n != 4 {
		t.Fatalf("%d arrived, want 4", n)
	}
	if n := spooled(t, c); n != 0 {
		t.Fatalf("%d still spooled", n)
	}
}

// An address nothing listens on fails too, and keeps the event.
func TestUnreachableKeeps(t *testing.T) {
	_, srv := newFake()
	url := srv.URL
	srv.Close()
	c := newClient(t, url, &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)})
	_ = c.Record(Event{Type: Start})
	if err := c.Flush(context.Background()); err == nil || spooled(t, c) != 1 {
		t.Fatalf("flush to a closed port: err=%v, spooled=%d; want an error and 1", err, spooled(t, c))
	}
}

func TestRetryAfterAmbiguousFailureIsHarmless(t *testing.T) {
	f, srv := newFake()
	defer srv.Close()
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	c := newClient(t, srv.URL, clk)
	f.set(func(f *fakeWorker) { f.storeAnd = http.StatusBadGateway })
	for i := range 60 {
		_ = c.Record(Event{Type: Start, Op: fmt.Sprintf("op-%08d", i)})
	}
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("a 502 counted as sent")
	}
	f.set(func(f *fakeWorker) { f.storeAnd = 0 })
	clk.add(2 * time.Minute)
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, reqs := f.count()
	if n != 60 {
		t.Fatalf("%d distinct events, want 60", n)
	}
	// 1 failed batch, then all three batches of at most 25.
	if reqs != 4 {
		t.Errorf("%d requests, want 4 (one failed, then three batches)", reqs)
	}
}

func TestPoisonBatchIsDropped(t *testing.T) {
	f, srv := newFake()
	defer srv.Close()
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	c := newClient(t, srv.URL, clk)
	f.set(func(f *fakeWorker) { f.status = http.StatusBadRequest })
	_ = c.Record(Event{Type: Start})
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spooled(t, c) != 0 {
		t.Error("a batch the Worker refuses as malformed is kept, and would block the spool forever")
	}
	f.set(func(f *fakeWorker) { f.status = http.StatusUnauthorized })
	_ = c.Record(Event{Type: Start})
	_ = c.Flush(context.Background())
	if spooled(t, c) != 1 {
		t.Error("a 401 dropped the event; a fixed token should still send it")
	}
}

func TestDrainIsBounded(t *testing.T) {
	f, srv := newFake()
	defer srv.Close()
	f.set(func(f *fakeWorker) { f.hang = 10 * time.Second })
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	c := newClient(t, srv.URL, clk)
	_ = c.Record(Event{Type: Start})
	c.FlushAsync()
	began := time.Now()
	c.Drain(300 * time.Millisecond)
	if d := time.Since(began); d > time.Second {
		t.Fatalf("Drain took %s against a hanging Worker, want about 300ms", d)
	}
	if spooled(t, c) != 1 {
		t.Error("the event was lost when the send timed out")
	}
	// The background send is still blocked on the hanging fake. End it and
	// wait, or it writes its back-off file while the temp dir is being
	// removed ("directory not empty", 1 in 3 runs).
	srv.CloseClientConnections()
	c.wg.Wait()
}

func TestConcurrentRecordsDuringFlushAreKept(t *testing.T) {
	f, srv := newFake()
	defer srv.Close()
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	c := newClient(t, srv.URL, clk)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 25 {
				_ = c.Record(Event{Type: Start, Op: fmt.Sprintf("op-%02d-%05d", g, i)})
				if i%5 == 0 {
					_ = c.Flush(context.Background())
				}
			}
		})
	}
	wg.Wait()
	_ = c.Flush(context.Background())
	if n, _ := f.count(); n != 200 {
		t.Fatalf("%d of 200 events arrived", n)
	}
}

func TestRepoOf(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "proj")
	_ = os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:joeblew999/irgo-windows-vm.git\n"), 0o644)
	sub := filepath.Join(repo, "a", "b")
	_ = os.MkdirAll(sub, 0o755)
	if got := repoOf(sub); got != "joeblew999/irgo-windows-vm" {
		t.Errorf("repoOf(subdir) = %q", got)
	}
	wt := filepath.Join(root, "elsewhere", "wt")
	_ = os.MkdirAll(wt, 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644)
	if got := repoOf(wt); got != "joeblew999/irgo-windows-vm" {
		t.Errorf("repoOf(worktree) = %q, want the repository's name", got)
	}
	_ = os.Remove(filepath.Join(repo, ".git", "config"))
	if got := repoOf(sub); got != "proj" {
		t.Errorf("repoOf with no remote = %q, want the directory name", got)
	}
}
