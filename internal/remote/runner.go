package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
)

// Executor runs one job on this Mac. The serve command's executor clones a
// VM, runs the binary in it through app-create, photographs it and deletes
// it; the tests' executor is a fake.
type Executor interface {
	// Run runs j, whose binary has been downloaded to exe and checked. Every
	// line written to log reaches the job's log. ctx is cancelled when the job
	// is cancelled or its lease is lost; Run stops at the next step it can,
	// and still cleans up.
	Run(ctx context.Context, j Job, exe string, log io.Writer) Outcome
}

// Outcome is what a run produced.
type Outcome struct {
	Code    command.Code
	Message string
	Files   map[string][]byte // result files by name: stdout.txt, test2json.json, *.png
}

// ServeOptions tunes the loop. Zero values are the defaults.
type ServeOptions struct {
	Poll      time.Duration // between claims when the queue is empty (3 s)
	Heartbeat time.Duration // between heartbeats while a job runs (20 s; the lease is 90 s)
	Flush     time.Duration // between log uploads while a job runs (3 s)
	Once      bool          // return after one job instead of serving forever
	WorkDir   string        // where binaries are downloaded; a temporary directory if empty
	Say       func(string, ...any)
}

func (o *ServeOptions) defaults() {
	if o.Poll <= 0 {
		o.Poll = 3 * time.Second
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 20 * time.Second
	}
	if o.Flush <= 0 {
		o.Flush = 3 * time.Second
	}
	if o.Say == nil {
		o.Say = func(string, ...any) {}
	}
}

// Serve takes jobs from the Worker and runs them, one at a time, until ctx
// ends (or, with Once, after one job). A refused token ends it; anything else
// the Worker answers is retried with a growing pause, so a Mac that loses its
// network picks up again when it comes back.
func Serve(ctx context.Context, c *Client, ex Executor, o ServeOptions) error {
	o.defaults()
	pause := o.Poll
	o.Say("serving %s as runner %q: polling every %s, one job at a time", c.URL, c.Runner, o.Poll)
	for {
		j, err := c.Claim(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrAuth):
			return err
		case err != nil:
			pause = min(pause*2, time.Minute)
			o.Say("claiming a job: %v; trying again in %s", err, pause)
		case j == nil:
			pause = o.Poll
		default:
			pause = o.Poll
			RunJob(ctx, c, ex, *j, o)
			if o.Once {
				return nil
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}
	}
}

// logBuf is a job's log: written by the executor, uploaded by the loop.
type logBuf struct {
	mu   sync.Mutex
	b    bytes.Buffer
	sent int
}

// maxLog is a little under the 2 MiB the Worker keeps.
const maxLog = 2<<20 - 1024

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Past the limit the rest is dropped, and the head, which names the
	// clone and the binary, is kept.
	if l.b.Len()+len(p) > maxLog {
		return len(p), nil
	}
	return l.b.Write(p)
}

// changed returns the whole log if it grew since the last call.
func (l *logBuf) changed() ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() == l.sent {
		return nil, false
	}
	l.sent = l.b.Len()
	return append([]byte(nil), l.b.Bytes()...), true
}

// RunJob runs one claimed job and reports it: the binary down, the executor
// under a heartbeat, the log up as it grows, the files, then the result.
// Whatever happens, the job ends reported or with its lease left to run out,
// which the Worker turns into "lost".
func RunJob(ctx context.Context, c *Client, ex Executor, j Job, o ServeOptions) {
	o.defaults()
	say := o.Say
	start := time.Now()
	say("job %s from %s: %s %s (gui=%v, %d args, timeout %ds)", j.ID, j.Owner, j.Spec.Kind, j.Spec.Name, j.Spec.GUI, len(j.Spec.Args), j.Spec.TimeoutS)
	log := &logBuf{}
	logf := func(format string, a ...any) {
		_, _ = fmt.Fprintf(log, "[%6.1fs] serve: "+format+"\n", append([]any{time.Since(start).Seconds()}, a...)...)
	}
	logf("job %s, runner %s", j.ID, c.Runner)

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		beat, flush := time.NewTicker(o.Heartbeat), time.NewTicker(o.Flush)
		defer beat.Stop()
		defer flush.Stop()
		for {
			select {
			case <-done:
				return
			case <-flush.C:
				if b, ok := log.changed(); ok {
					if err := c.PutLog(ctx, j.ID, b); err != nil {
						say("job %s: uploading the log: %v", j.ID, err)
					}
				}
			case <-beat.C:
				stop, err := c.Heartbeat(ctx, j.ID)
				switch {
				case errors.Is(err, ErrGone):
					cancel(fmt.Errorf("the Worker says the job is over: %w", err))
				case err != nil:
					say("job %s: heartbeat: %v", j.ID, err)
				case stop:
					cancel(errors.New("cancelled by its caller"))
				}
			}
		}
	}()

	out := runOne(runCtx, c, ex, j, o.WorkDir, log, logf)
	if runCtx.Err() != nil {
		cause := context.Cause(runCtx)
		logf("stopped: %v", cause)
		if out.Code == command.CodeOK {
			out.Code, out.Message = command.CodeNotRun, cause.Error()
		}
	}
	close(done)
	wg.Wait()

	res := report(ctx, c, j, out, logf)
	logf("finished: exit %d (%s) in %s", res.ExitCode, res.Outcome, time.Since(start).Round(time.Second))
	if b, ok := log.changed(); ok {
		if err := c.PutLog(ctx, j.ID, b); err != nil {
			say("job %s: uploading the log: %v", j.ID, err)
		}
	}
	fin, err := c.Finish(ctx, j.ID, res)
	if err != nil {
		say("job %s: reporting the result: %v", j.ID, err)
		return
	}
	say("job %s: %s, exit %d (%s), %d files, %s", j.ID, fin.State, res.ExitCode, res.Outcome, len(res.Files), time.Since(start).Round(time.Second))
}

// runOne downloads the binary and runs the executor. A binary that does not
// arrive intact is not run.
func runOne(ctx context.Context, c *Client, ex Executor, j Job, workDir string, log io.Writer, logf func(string, ...any)) Outcome {
	if filepath.Base(j.Spec.Name) != j.Spec.Name || j.Spec.Name == "" {
		return Outcome{Code: command.CodeUsage, Message: fmt.Sprintf("%q is not a file name", j.Spec.Name)}
	}
	dir, err := os.MkdirTemp(workDir, "irgo-job-")
	if err != nil {
		return Outcome{Code: command.CodeNotRun, Message: "creating a work directory: " + err.Error()}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	exe := filepath.Join(dir, j.Spec.Name)
	logf("downloading %s (%d bytes)", j.Spec.Name, j.Spec.Size)
	if err := c.Input(ctx, j, exe); err != nil {
		return Outcome{Code: command.CodeNotRun, Message: "downloading the binary: " + err.Error()}
	}
	logf("verified SHA-256 %s", j.Spec.SHA256)
	return ex.Run(ctx, j, exe, log)
}

// report stores the outcome's files and result.json, and returns the result
// to finish with. A file that does not upload is named in the message, not
// silently dropped.
func report(ctx context.Context, c *Client, j Job, out Outcome, logf func(string, ...any)) Result {
	o, _ := command.Classify(out.Code)
	res := Result{ExitCode: int(out.Code), Outcome: o.Name, Message: out.Message}
	names := make([]string, 0, len(out.Files))
	for n := range out.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := c.PutFile(ctx, j.ID, n, out.Files[n]); err != nil {
			logf("result file %s did not upload: %v", n, err)
			res.Message += fmt.Sprintf(" [%s not uploaded: %v]", n, err)
			continue
		}
		logf("result file %s (%d bytes)", n, len(out.Files[n]))
		res.Files = append(res.Files, n)
	}
	summary, err := json.MarshalIndent(map[string]any{
		"job": j.ID, "spec": j.Spec, "runner": c.Runner,
		"exit_code": res.ExitCode, "outcome": res.Outcome, "message": res.Message, "files": res.Files,
	}, "", "  ")
	if err == nil && c.PutFile(ctx, j.ID, "result.json", append(summary, '\n')) == nil {
		res.Files = append(res.Files, "result.json")
	}
	return res
}
