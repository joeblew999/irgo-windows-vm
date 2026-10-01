// Package remote drives a Mac somewhere else: the client of the Worker's job
// queue (worker/jobs.go), for `irgo-winvm remote ...` on any OS, and the loop
// `irgo-winvm serve` runs on a Mac to take jobs from it.
//
// The Mac only connects out. It asks the Worker for the oldest queued job,
// runs it on a fresh clone of the golden image, and sends the log, the result
// files and the exit code back. The Worker holds the queue in R2; nothing
// here listens on a port.
//
// It knows nothing about UTM: what running a job means is the Executor the
// serve command passes in, which is the CLI's own commands. That keeps this
// package building and testing on Linux and Windows, where the client runs.
package remote

import (
	"errors"
	"fmt"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
)

// GuideURL is the page that says how to set the remote up and use it.
const GuideURL = "https://joeblew999.github.io/irgo-windows-vm/agents.html#from-another-machine-linux-windows-github"

// The environment the client and the Mac read. GuideURL says how each is set.
const (
	EnvURL         = "IRGO_REMOTE_URL"          // the Worker, e.g. https://irgo-windows-vm.<you>.workers.dev
	EnvToken       = "IRGO_REMOTE_TOKEN"        // a caller's token (JOBS_TOKENS), or the admin token
	EnvRunnerToken = "IRGO_REMOTE_RUNNER_TOKEN" // the Mac's token (JOBS_RUNNER_TOKEN), for serve only
)

// Spec is what a caller asks for: the Worker's JobSpec, field for field.
type Spec struct {
	Kind     string   `json:"kind"` // KindApp or KindTest
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	GUI      bool     `json:"gui,omitempty"`
	Args     []string `json:"args,omitempty"`
	TimeoutS int      `json:"timeout_s,omitempty"`
}

// Job kinds.
const (
	KindApp  = "app"  // an .exe: run, its output and exit code back
	KindTest = "test" // a `go test -c` binary: run with -test.v=test2json, events back
)

// OutToken in an argument is replaced on the Mac by a directory in the
// guest. A test that writes a file there and logs `screenshot: <path>`
// relative to it (the conformance suite's convention) gets the file back as
// a result.
const OutToken = "{out}"

// FileInfo is one stored result file.
type FileInfo struct {
	Name   string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Job is a job as the Worker reports it.
type Job struct {
	ID       string     `json:"id"`
	Owner    string     `json:"owner"`
	Spec     Spec       `json:"spec"`
	State    string     `json:"state"`
	Created  time.Time  `json:"created"`
	Queued   time.Time  `json:"queued,omitzero"`
	Started  time.Time  `json:"started,omitzero"`
	Finished time.Time  `json:"finished,omitzero"`
	Runner   string     `json:"runner,omitempty"`
	Cancel   bool       `json:"cancel_requested,omitempty"`
	ExitCode *int       `json:"exit_code,omitempty"`
	Outcome  string     `json:"outcome,omitempty"`
	Message  string     `json:"message,omitempty"`
	Files    []FileInfo `json:"files,omitempty"`
	Why      string     `json:"why,omitempty"`
	Position int        `json:"position,omitempty"`
	Running  int        `json:"running"`
}

// Job states, as the Worker names them.
const (
	StateUploading = "uploading"
	StateQueued    = "queued"
	StateRunning   = "running"
	StateFinished  = "finished"
	StateCancelled = "cancelled"
	StateExpired   = "expired"
	StateLost      = "lost"
	StateTimedOut  = "timed-out"
)

// Final reports whether the job has ended, one way or another.
func (j Job) Final() bool {
	return j.State != StateUploading && j.State != StateQueued && j.State != StateRunning
}

// Code is the exit code a job maps to on the tool's table
// (command.Outcomes): what the Mac's command exited with when the job ran,
// and CodeNotRun when it never ran or never finished: cancelled, expired in
// the queue, its Mac gone. A code the Mac reported that this build does not
// declare is CodeFailed, never passed through as if it meant something here.
func (j Job) Code() command.Code {
	if j.State != StateFinished || j.ExitCode == nil {
		return command.CodeNotRun
	}
	c := command.Code(*j.ExitCode)
	if _, ok := command.Classify(c); !ok {
		return command.CodeFailed
	}
	return c
}

// Err is the job's outcome as an error: nil when it exited 0, otherwise one
// that carries its code (see Code) and says why.
func (j Job) Err() error {
	c := j.Code()
	if c == command.CodeOK {
		return nil
	}
	why := j.Message
	if j.State != StateFinished {
		why = j.State
		if j.Why != "" {
			why += ": " + j.Why
		}
	}
	if why == "" {
		why = "no message"
	}
	return &JobError{ID: j.ID, Code: c, Msg: why}
}

// JobError is a job that did not succeed.
type JobError struct {
	ID   string
	Code command.Code
	Msg  string
}

func (e *JobError) Error() string {
	o, _ := command.Classify(e.Code)
	return fmt.Sprintf("job %s: %s (exit %d, %s)", e.ID, e.Msg, e.Code, o.Name)
}

// Errors the client classifies, so the CLI can map them to exit codes.
var (
	// ErrConfig is IRGO_REMOTE_URL or a token missing.
	ErrConfig = errors.New("remote is not configured")
	// ErrAuth is the Worker refusing the token: 401 or 403.
	ErrAuth = errors.New("the Worker refused the token")
	// ErrNotFound is a job that does not exist, or is not this caller's.
	ErrNotFound = errors.New("no such job")
	// ErrGone is a runner call about a job that is no longer running: it was
	// cancelled, or its lease ran out. The Mac stops it.
	ErrGone = errors.New("the job is no longer running")
)
