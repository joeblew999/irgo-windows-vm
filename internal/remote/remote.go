// Package remote drives a Mac somewhere else: the client side of the
// Worker's job queue, for `irgo-winvm remote-*` on any OS, and the loop
// `irgo-winvm serve` runs on a Mac to take jobs from it. The requests are
// internal/workerclient's, built from wire's route table; this package waits,
// prints, checks and maps outcomes onto the tool's exit codes.
//
// The Mac only connects out. It asks the Worker for the oldest queued job,
// runs it on a fresh clone of the golden image, and sends the log, the result
// files and the exit code back. Nothing here listens on a port.
//
// It knows nothing about UTM: what running a job means is the Executor the
// serve command passes in, which is the CLI's own code. That keeps this
// package building and testing on Linux and Windows, where the client runs.
package remote

import (
	"errors"
	"fmt"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// GuideURL is the page that says how to set the remote up and use it.
const GuideURL = "https://joeblew999.github.io/irgo-windows-vm/agents.html#from-another-machine-linux-windows-github"

// The types are wire's: what the routes send and answer.
type (
	Spec     = wire.JobSpec
	Job      = wire.JobView
	FileInfo = wire.BlobInfo // Key is the file's name
)

// Job kinds.
const (
	KindApp  = wire.JobKindApp
	KindTest = wire.JobKindTest
)

// OutToken in an argument is replaced on the Mac by a directory in the
// guest. A test that writes a file there and logs `screenshot: <path>`
// relative to it (the conformance suite's convention) gets the file back as
// a result.
const OutToken = "{out}"

// Code is the exit code a job maps to on the tool's table
// (command.Outcomes): what the Mac's command exited with when the job ran,
// and CodeNotRun when it never ran or never finished: cancelled, expired in
// the queue, its Mac gone. A code the Mac reported that this build does not
// declare is CodeFailed, never passed through as if it meant something here.
func Code(j Job) command.Code {
	if j.State != wire.JobFinished || j.ExitCode == nil {
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
func Err(j Job) error {
	c := Code(j)
	if c == command.CodeOK {
		return nil
	}
	why := j.Message
	if j.State != wire.JobFinished {
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
	// ErrAuth is the Worker refusing the token, or not configured for it.
	ErrAuth = errors.New("the Worker refused the token")
	// ErrNotFound is a job that does not exist, or is not this caller's.
	ErrNotFound = errors.New("no such job")
	// ErrGone is a runner call about a job that is no longer running: it was
	// cancelled, or its lease ran out. The Mac stops it.
	ErrGone = errors.New("the job is no longer running")
)
