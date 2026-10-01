package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
)

// fakeWorker is the Worker's job queue, as much of it as the client and the
// runner touch, in memory. The real one is tested in worker/jobs_test.go;
// this checks the Go side speaks to it as the routes say.
type fakeWorker struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	input   map[string][]byte
	logs    map[string][]byte
	files   map[string][]byte
	corrupt bool // send the runner a damaged binary
	cancel  bool // answer heartbeats with cancel
	beats   int
}

func newFake(t *testing.T) (*fakeWorker, *httptest.Server) {
	f := &fakeWorker{jobs: map[string]*Job{}, input: map[string][]byte{}, logs: map[string][]byte{}, files: map[string][]byte{}}
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	return f, s
}

func (f *fakeWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	send := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	if tok != "sub" && tok != "run" {
		send(401, map[string]string{"error": "refused: no valid bearer token"})
		return
	}
	if (p[0] == "runner") != (tok == "run") {
		send(403, map[string]string{"error": "wrong role"})
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case p[0] == "jobs" && len(p) == 1 && r.Method == "POST":
		var s Spec
		_ = json.Unmarshal(body, &s)
		id := strings.Repeat("a", 31) + strconv.Itoa(len(f.jobs))
		f.jobs[id] = &Job{ID: id, Owner: "me", Spec: s, State: StateUploading}
		send(201, map[string]any{"job": f.jobs[id]})
	case p[0] == "jobs" && len(p) >= 2:
		j, ok := f.jobs[p[1]]
		if !ok {
			send(404, map[string]string{"error": "no such job"})
			return
		}
		switch {
		case len(p) == 2:
			send(200, j)
		case p[2] == "input":
			f.input[j.ID], j.State, j.Position = body, StateQueued, 1
			send(200, j)
		case p[2] == "cancel":
			j.State = StateCancelled
			send(200, j)
		case p[2] == "log":
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			l := f.logs[j.ID]
			w.Header().Set("X-Log-Size", strconv.Itoa(len(l)))
			if off < len(l) {
				_, _ = w.Write(l[off:])
			}
		case p[2] == "files":
			b, ok := f.files[j.ID+"/"+p[3]]
			if !ok {
				send(404, map[string]string{"error": "no such file"})
				return
			}
			s := sha256.Sum256(b)
			w.Header().Set("X-Job-Sha256", hex.EncodeToString(s[:]))
			_, _ = w.Write(b)
		}
	case p[0] == "runner" && p[1] == "claim":
		for _, j := range f.jobs {
			if j.State == StateQueued {
				j.State, j.Runner = StateRunning, r.Header.Get("X-Runner")
				send(200, j)
				return
			}
		}
		w.WriteHeader(204)
	case p[0] == "runner" && p[1] == "jobs":
		j, ok := f.jobs[p[2]]
		if !ok || j.State != StateRunning {
			send(409, map[string]string{"error": "the job is gone, not running"})
			return
		}
		switch p[3] {
		case "heartbeat":
			f.beats++
			send(200, map[string]any{"cancel": f.cancel})
		case "input":
			b := append([]byte(nil), f.input[j.ID]...)
			if f.corrupt {
				b[0] ^= 1
			}
			_, _ = w.Write(b)
		case "log":
			f.logs[j.ID] = body
			send(201, map[string]any{})
		case "files":
			f.files[j.ID+"/"+p[4]] = body
			send(201, map[string]any{})
		case "finish":
			var res Result
			_ = json.Unmarshal(body, &res)
			j.State, j.ExitCode, j.Outcome, j.Message = StateFinished, &res.ExitCode, res.Outcome, res.Message
			if f.cancel {
				j.State = StateCancelled
			}
			j.Files = nil
			for _, n := range res.Files {
				j.Files = append(j.Files, FileInfo{Name: n, Size: int64(len(f.files[j.ID+"/"+n]))})
			}
			send(200, j)
		}
	default:
		send(404, map[string]string{"error": "no such endpoint"})
	}
}

func intp(n int) *int { return &n }

// Negative control (by hand, 1 Oct 2026): returning *j.ExitCode whatever the
// state fails the "lost" and "cancelled" rows; dropping the Classify check
// fails "undeclared". Restored.
func TestJobCodeMapsOntoTheToolsTable(t *testing.T) {
	for _, c := range []struct {
		name string
		j    Job
		want command.Code
	}{
		{"ran and passed", Job{State: StateFinished, ExitCode: intp(0)}, command.CodeOK},
		{"program failed", Job{State: StateFinished, ExitCode: intp(1)}, command.CodeFailed},
		{"VM gone on the Mac", Job{State: StateFinished, ExitCode: intp(3)}, command.CodeNoVM},
		{"agent away on the Mac", Job{State: StateFinished, ExitCode: intp(4)}, command.CodeNoAgent},
		{"busy on the Mac", Job{State: StateFinished, ExitCode: intp(6)}, command.CodeBusy},
		{"undeclared", Job{State: StateFinished, ExitCode: intp(42)}, command.CodeFailed},
		{"finished with no code", Job{State: StateFinished}, command.CodeNotRun},
		{"lost", Job{State: StateLost, ExitCode: intp(0)}, command.CodeNotRun},
		{"cancelled", Job{State: StateCancelled, ExitCode: intp(0)}, command.CodeNotRun},
		{"expired", Job{State: StateExpired}, command.CodeNotRun},
	} {
		if got := c.j.Code(); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
		if err := c.j.Err(); (err == nil) != (c.want == command.CodeOK) {
			t.Errorf("%s: Err() = %v", c.name, err)
		}
	}
	if o, ok := command.Classify(command.CodeNotRun); !ok || !o.Retryable {
		t.Fatal("not-run must be declared and retryable: submitting again can work")
	}
}

func TestClientClassifiesRefusals(t *testing.T) {
	_, s := newFake(t)
	ctx := context.Background()
	bad := &Client{URL: s.URL, Token: "nope"}
	if _, err := bad.Status(ctx, "x"); !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "no valid bearer token") {
		t.Errorf("a refused token: %v, want ErrAuth with the Worker's message", err)
	}
	sub := &Client{URL: s.URL, Token: "sub"}
	if _, err := sub.Status(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing job: %v, want ErrNotFound", err)
	}
	if _, err := sub.Claim(ctx); !errors.Is(err, ErrAuth) {
		t.Errorf("a caller claiming: %v, want ErrAuth", err)
	}
	run := &Client{URL: s.URL, Token: "run"}
	if _, err := run.Heartbeat(ctx, "missing"); !errors.Is(err, ErrGone) {
		t.Errorf("a heartbeat for a job that is not running: %v, want ErrGone", err)
	}
	if err := bad.callJSON(ctx, "GET", "/api/nothing", nil, nil); errors.Is(err, ErrGone) {
		t.Error("a caller's 4xx was classified as the runner's ErrGone")
	}
}

func TestFromEnvNamesWhatIsMissing(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvToken, "")
	_, err := FromEnv(EnvToken)
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), EnvURL) || !strings.Contains(err.Error(), EnvToken) {
		t.Fatalf("FromEnv with nothing set: %v", err)
	}
}

// fakeExec records what it was given and returns a fixed outcome.
type fakeExec struct {
	ran      []byte
	out      Outcome
	untilCtx bool // block until the job is cancelled
}

func (e *fakeExec) Run(ctx context.Context, j Job, exe string, log io.Writer) Outcome {
	e.ran, _ = os.ReadFile(exe)
	_, _ = io.WriteString(log, "fake: ran "+j.Spec.Name+"\n")
	if e.untilCtx {
		select {
		case <-ctx.Done():
			return Outcome{Code: command.CodeNotRun, Message: context.Cause(ctx).Error()}
		case <-time.After(5 * time.Second):
			return Outcome{Code: command.CodeOK, Message: "never cancelled"}
		}
	}
	return e.out
}

func writeExe(t *testing.T) (string, []byte) {
	t.Helper()
	b := []byte("MZ a pretend Windows binary")
	p := filepath.Join(t.TempDir(), "hello.exe")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, b
}

// submitted puts a job in the fake through the client, as `remote submit`
// does.
func submitted(t *testing.T, s *httptest.Server, gui bool) (*Client, Job, []byte) {
	t.Helper()
	ctx := context.Background()
	path, b := writeExe(t)
	spec, err := SpecFor(path, KindApp, gui, []string{"-x"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{URL: s.URL, Token: "sub"}
	j, err := c.Submit(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if j, err = c.Upload(ctx, j.ID, path); err != nil || j.State != StateQueued {
		t.Fatalf("upload: %v %+v", err, j)
	}
	return c, j, b
}

func TestServeRunsAJobAndReportsIt(t *testing.T) {
	f, s := newFake(t)
	c, j, b := submitted(t, s, true)
	ex := &fakeExec{out: Outcome{Code: command.CodeFailed, Message: "hello.exe exited 3 in the guest",
		Files: map[string][]byte{"stdout.txt": []byte("hi\n"), "desktop.png": []byte("\x89PNG")}}}
	run := &Client{URL: s.URL, Token: "run", Runner: "mac-test"}
	if err := Serve(context.Background(), run, ex, ServeOptions{Once: true, Poll: 10 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ex.ran, b) {
		t.Fatalf("the executor ran %q, not the submitted binary", ex.ran)
	}
	var out bytes.Buffer
	final, err := Wait(context.Background(), c, j.ID, &out, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if final.Code() != command.CodeFailed || final.Runner != "mac-test" || !strings.Contains(final.Message, "exited 3") {
		t.Fatalf("final job: %+v", final)
	}
	if !strings.Contains(out.String(), "fake: ran hello.exe") {
		t.Fatalf("Wait did not print the Mac's log:\n%s", out.String())
	}
	dir := t.TempDir()
	paths, err := Fetch(context.Background(), c, final, dir)
	if err != nil || len(paths) != 3 {
		t.Fatalf("fetch: %v %v (want stdout.txt, desktop.png, result.json)", err, paths)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "stdout.txt")); string(got) != "hi\n" {
		t.Fatalf("stdout.txt: %q", got)
	}
	if !strings.Contains(string(f.files[j.ID+"/result.json"]), `"outcome": "failed"`) {
		t.Fatalf("result.json: %s", f.files[j.ID+"/result.json"])
	}
}

// Negative control (by hand, 1 Oct 2026): ignoring the checksum in
// writeChecked lets the executor run the damaged binary and fails this.
// Restored.
func TestADamagedBinaryIsNeverRun(t *testing.T) {
	f, s := newFake(t)
	c, j, _ := submitted(t, s, false)
	f.corrupt = true
	ex := &fakeExec{out: Outcome{Code: command.CodeOK}}
	if err := Serve(context.Background(), &Client{URL: s.URL, Token: "run"}, ex, ServeOptions{Once: true}); err != nil {
		t.Fatal(err)
	}
	if ex.ran != nil {
		t.Fatal("the executor ran a binary that did not match its SHA-256")
	}
	final, _ := c.Status(context.Background(), j.ID)
	if final.Code() != command.CodeNotRun || !strings.Contains(final.Message, "SHA-256") {
		t.Fatalf("final: %+v", final)
	}
}

// Negative control (by hand, 1 Oct 2026): not cancelling runCtx on a
// heartbeat that says cancel leaves the executor running for its full 5 s
// and fails this. Restored.
func TestACancelledJobStops(t *testing.T) {
	f, s := newFake(t)
	c, j, _ := submitted(t, s, false)
	f.cancel = true
	ex := &fakeExec{untilCtx: true}
	start := time.Now()
	err := Serve(context.Background(), &Client{URL: s.URL, Token: "run"}, ex,
		ServeOptions{Once: true, Heartbeat: 20 * time.Millisecond, Flush: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the cancelled job ran on for %s", time.Since(start))
	}
	final, _ := c.Status(context.Background(), j.ID)
	if final.State != StateCancelled || final.Code() != command.CodeNotRun || !strings.Contains(final.Message, "cancelled") {
		t.Fatalf("final: %+v", final)
	}
}

func TestServeStopsOnARefusedToken(t *testing.T) {
	_, s := newFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Serve(ctx, &Client{URL: s.URL, Token: "wrong"}, &fakeExec{}, ServeOptions{Poll: time.Millisecond})
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Serve with a refused token: %v, want ErrAuth and to stop", err)
	}
}

func TestSpecForRefusesWhatIsNotAWindowsBinary(t *testing.T) {
	dir := t.TempDir()
	elf := filepath.Join(dir, "app.exe")
	_ = os.WriteFile(elf, []byte("\x7fELF..."), 0o644)
	if _, err := SpecFor(elf, KindApp, false, nil, time.Minute); err == nil {
		t.Error("an ELF named .exe was accepted")
	}
	noExt := filepath.Join(dir, "app")
	_ = os.WriteFile(noExt, []byte("MZ"), 0o644)
	if _, err := SpecFor(noExt, KindApp, false, nil, time.Minute); err == nil {
		t.Error("a binary without .exe was accepted")
	}
}
