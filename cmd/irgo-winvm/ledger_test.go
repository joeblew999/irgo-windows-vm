package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/ledger"
)

// recorder is the Worker's ingest, keeping what it was sent.
type recorder struct {
	mu     sync.Mutex
	events []ledger.Event
}

func (rc *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct{ Events []ledger.Event }
	_ = json.NewDecoder(r.Body).Decode(&in)
	rc.mu.Lock()
	rc.events = append(rc.events, in.Events...)
	rc.mu.Unlock()
	_, _ = w.Write([]byte(`{}`))
}

func useLedger(t *testing.T, url string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // not the real locks, jobs or spool
	ledger.Configure(ledger.New(ledger.Config{URL: url, Token: "t", Dir: filepath.Join(t.TempDir(), "ledger"), Version: "test"}))
	t.Cleanup(func() { ledger.Configure(nil) })
}

func TestCommandsAreRecorded(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	useLedger(t, srv.URL)

	err := run([]string{"app-create"}) // fails its own argument check: exit 2
	if err == nil {
		t.Fatal("app-create with no binary succeeded")
	}
	_ = run([]string{"version"}) // not recorded
	_ = runToolFor("claude-code", "status", nil)
	ledger.DrainDefault(2 * time.Second)

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.events) != 4 {
		t.Fatalf("%d events, want start and end of app-create and status: %+v", len(rc.events), rc.events)
	}
	byType := map[string]ledger.Event{}
	for _, e := range rc.events {
		byType[e.Command+"/"+string(e.Type)] = e
	}
	s, e := byType["app-create/start"], byType["app-create/end"]
	if s.Op == "" || s.Op != e.Op || s.VM == "" || e.Exit == nil || *e.Exit != int64(exitCode(err)) || e.DurationMS == nil || e.Detail == "" {
		t.Errorf("app-create: start %+v, end %+v", s, e)
	}
	if st := byType["status/end"]; st.Client != "claude-code" || st.VM != "" || st.Exit == nil || *st.Exit != 0 {
		t.Errorf("status over MCP: %+v", st)
	}
}

// A detached job started over MCP is a new process with no MCP connection:
// it records the client the server handed it in job.ClientEnv, not "cli".
// The command line, with nothing handed over, is still "cli".
//
// Negative control (by hand, 1 Oct 2026): making runTool pass "" again fails
// this with client "cli" for the job child; restored.
func TestAJobChildRecordsItsMCPClient(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	useLedger(t, srv.URL)

	t.Setenv(job.ClientEnv, "claude-code")
	_ = runTool("status", nil) // as the job child runs its command
	t.Setenv(job.ClientEnv, "")
	_ = run([]string{"status"})
	ledger.DrainDefault(2 * time.Second)

	rc.mu.Lock()
	defer rc.mu.Unlock()
	var clients []string
	for _, e := range rc.events {
		if e.Type == ledger.End {
			clients = append(clients, e.Client)
		}
	}
	if len(clients) != 2 || clients[0] != "claude-code" || clients[1] != "cli" {
		t.Fatalf("clients of the two ends: %q, want the job child's claude-code then the command line's cli", clients)
	}
}

// The negative control the ledger must keep: a Worker that is down, or that
// hangs, never changes what a command returns, and the command itself never
// waits on it. Checked against the same commands with the ledger off.
//
// Negative control (by hand, 1 Oct 2026): making runToolFor drain the ledger
// before returning fails this with "the command waited on the ledger" for
// the hanging Worker; restored.
func TestWorkerOutageNeverChangesTheExitCode(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(30 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()

	cases := [][]string{{"app-create"}, {"status"}, {"iso-create", "-no-such-flag"}, {"no-such-command"}}
	want := map[int]int{}
	base := map[int]time.Duration{}
	for i, args := range cases {
		t.Setenv("HOME", t.TempDir())
		ledger.Configure(nil)
		began := time.Now()
		want[i] = int(exitCode(run(args)))
		base[i] = time.Since(began)
	}
	for name, url := range map[string]string{"hanging": hang.URL, "down": downURL, "500": failing.URL} {
		for i, args := range cases {
			useLedger(t, url)
			began := time.Now()
			got := int(exitCode(run(args)))
			took := time.Since(began)
			drainBegan := time.Now()
			ledger.DrainDefault(300 * time.Millisecond)
			if got != want[i] {
				t.Errorf("Worker %s: %v exited %d, %d with the ledger off", name, args, got, want[i])
			}
			// Relative to the same command with the ledger off: `status` asks
			// UTM, which alone took over a second under a full parallel test
			// run, so an absolute bound failed with no ledger wait at all.
			if took > 2*base[i]+time.Second {
				t.Errorf("Worker %s: %v took %s (%s with the ledger off); the command waited on the ledger", name, args, took, base[i])
			}
			if d := time.Since(drainBegan); d > time.Second {
				t.Errorf("Worker %s: draining took %s, over its budget", name, d)
			}
		}
	}
}
