package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/remote"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// The remote-* commands: the client of the Worker's job queue. They build and
// run on Linux, Windows and macOS alike, and change nothing locally except
// the files remote-result downloads. internal/remote does the talking.

const remoteSubmitAbout = `  Sends a Windows binary to a Mac running irgo-winvm serve, through the
  Worker. The Mac runs it on a fresh clone of its golden image and deletes
  the clone after. Needs IRGO_REMOTE_URL and IRGO_REMOTE_TOKEN.

    irgo-winvm remote submit [-gui] app.exe [args...]
    irgo-winvm remote submit -test -gui conformance.test.exe -test.run TestClipboard

  It waits, printing the Mac's log, then prints the program's output, saves
  the result files under -o, and exits with the job's exit code on the
  tool's table (` + utmvm.SiteURL + `using.html#what-it-exits-with); 8 means
  it never ran to the end. In args, {out} is a directory in the guest: a file a test
  writes there and names in a "screenshot: <path>" line comes back.
`

func remoteSubmitFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("remote-submit", flag.ContinueOnError)
	fs.Bool("gui", false, "run on the guest's desktop (required for anything with a window)")
	fs.Bool("test", false, "the binary is a `go test -c` test binary: run it with -test.v=test2json and return test2json events")
	fs.Duration("timeout", 10*time.Minute, "how long the program may run in the guest (at most 1h)")
	fs.Bool("wait", true, "wait for the result; -wait=false prints the job id and returns at once")
	fs.String("o", "irgo-remote", "directory the result files are saved in, under <job id>/")
	fs.Bool("json", false, "print the final job as JSON on the last line")
	return fs
}

func remoteClient() (*remote.Client, error) {
	c, err := remote.FromEnv(wire.ScopeJobs)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUsage, err)
	}
	return c, nil
}

// runRemoteSubmit uploads the binary, queues it, and with -wait follows it
// to the end and fetches the results.
func runRemoteSubmit(v values, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: irgo-winvm remote submit [-gui] [-test] <app.exe> [args...]", errUsage)
	}
	c, err := remoteClient()
	if err != nil {
		return err
	}
	kind := remote.KindApp
	if v.Bool("test") {
		kind = remote.KindTest
	}
	spec, err := remote.SpecFor(args[0], kind, v.Bool("gui"), args[1:], v.Duration("timeout"))
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	say := utmvm.Printer("remote-submit")
	ctx := context.Background()
	say("worker: %s", c.URL)
	say("binary: %s (%s, sha256 %s)", args[0], utmvm.HumanBytes(spec.Size), spec.SHA256[:16])
	j, err := c.Submit(ctx, spec)
	if err != nil {
		return remoteErr(err)
	}
	say("job:    %s", j.ID)
	t0 := time.Now()
	if j, err = c.Upload(ctx, j.ID, args[0]); err != nil {
		return remoteErr(err)
	}
	say("uploaded in %s; %s, %d ahead of it", time.Since(t0).Round(time.Millisecond), j.State, max(j.Position-1, 0))
	if !v.Bool("wait") {
		say("follow it: irgo-winvm remote logs -f %s", j.ID)
		say("then:      irgo-winvm remote result %s", j.ID)
		_, _ = fmt.Fprintln(utmvm.Out, j.ID)
		return nil
	}
	return followAndFetch(ctx, c, j.ID, v.String("o"), v.Bool("json"), say)
}

// followAndFetch waits for the job, prints its output, saves its files, and
// returns its outcome as an error carrying its exit code.
func followAndFetch(ctx context.Context, c *remote.Client, id, dir string, asJSON bool, say func(string, ...any)) error {
	final, err := remote.Wait(ctx, c, id, utmvm.Out, 0)
	if err != nil {
		return remoteErr(err)
	}
	return fetchAndReport(ctx, c, final, dir, asJSON, false, say)
}

// fetchAndReport saves a final job's files, prints its output, and returns
// its outcome. admin reads another caller's job's files with the admin token.
func fetchAndReport(ctx context.Context, c *remote.Client, j remote.Job, dir string, asJSON, admin bool, say func(string, ...any)) error {
	if len(j.Files) > 0 {
		dst := filepath.Join(dir, j.ID)
		paths, err := remote.Fetch(ctx, c, j, dst, admin)
		for _, p := range paths {
			say("saved %s", p)
		}
		if err != nil {
			return remoteErr(err)
		}
		if b, rErr := os.ReadFile(filepath.Join(dst, "stdout.txt")); rErr == nil && len(b) > 0 {
			say("--- the program's output (%s):", filepath.Join(dst, "stdout.txt"))
			_, _ = utmvm.Out.Write(b)
			if b[len(b)-1] != '\n' {
				_, _ = fmt.Fprintln(utmvm.Out)
			}
			say("---")
		}
	}
	say("job %s: %s on %s, exit %s", j.ID, j.State, j.Runner, exitWords(j))
	if asJSON {
		b, _ := json.Marshal(j)
		_, _ = fmt.Fprintln(utmvm.Out, string(b))
	}
	return remote.Err(j)
}

func exitWords(j remote.Job) string {
	if j.ExitCode == nil {
		return "none (" + j.Why + ")"
	}
	s := fmt.Sprintf("%d (%s)", *j.ExitCode, j.Outcome)
	if j.Message != "" {
		s += ": " + j.Message
	}
	return s
}

// remoteErr gives a client error the exit code a script can act on: a
// refused token or an unknown job is the command called wrongly.
func remoteErr(err error) error {
	switch {
	case errors.Is(err, remote.ErrAuth), errors.Is(err, remote.ErrNotFound), errors.Is(err, remote.ErrConfig):
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	return err
}

func remoteIDFlags(name string) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.Bool("json", false, "print the job as JSON")
		return fs
	}
}

func oneID(args []string, use string) (string, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("%w: irgo-winvm %s <job id>", errUsage, use)
	}
	return args[0], nil
}

// runRemoteStatus prints one job, or with no id and the admin token every
// job in the queue.
func runRemoteStatus(v values, args []string) error {
	c, err := remoteClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if len(args) == 0 {
		jobs, err := c.List(ctx)
		if err != nil {
			return remoteErr(err)
		}
		if v.Bool("json") {
			return json.NewEncoder(utmvm.Out).Encode(jobs)
		}
		for _, j := range jobs {
			_, _ = fmt.Fprintf(utmvm.Out, "%s  %-9s  %-8s  %s %s  %s\n", j.ID, j.State, j.Owner, j.Spec.Kind, j.Spec.Name, j.Created.Format(time.RFC3339))
		}
		return nil
	}
	id, err := oneID(args, "remote status")
	if err != nil {
		return err
	}
	j, err := c.Status(ctx, id)
	if err != nil {
		return remoteErr(err)
	}
	if v.Bool("json") {
		return json.NewEncoder(utmvm.Out).Encode(j)
	}
	say := utmvm.Reporter("remote-status")
	say("job:      %s (%s %s, from %s)", j.ID, j.Spec.Kind, j.Spec.Name, j.Owner)
	say("state:    %s", j.State)
	if j.State == wire.JobQueued {
		say("queue:    %d ahead of it, %d running", j.Position-1, j.Running)
	}
	if j.Runner != "" {
		say("runner:   %s", j.Runner)
	}
	if wire.JobFinal(j.State) {
		say("exit:     %s", exitWords(j))
		for _, f := range j.Files {
			say("file:     %s (%s)", f.Key, utmvm.HumanBytes(f.Size))
		}
	}
	return nil
}

func remoteLogsFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("remote-logs", flag.ContinueOnError)
	fs.Bool("f", false, "follow: keep printing until the job is final, then exit with its code")
	return fs
}

func runRemoteLogs(v values, args []string) error {
	id, err := oneID(args, "remote logs [-f]")
	if err != nil {
		return err
	}
	c, err := remoteClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if v.Bool("f") {
		j, err := remote.Wait(ctx, c, id, utmvm.Out, 0)
		if err != nil {
			return remoteErr(err)
		}
		return remote.Err(j)
	}
	b, _, err := c.Log(ctx, id, 0)
	if err != nil {
		return remoteErr(err)
	}
	_, _ = utmvm.Out.Write(b)
	return nil
}

func remoteResultFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("remote-result", flag.ContinueOnError)
	fs.String("o", "irgo-remote", "directory the result files are saved in, under <job id>/")
	fs.Bool("json", false, "print the final job as JSON on the last line")
	fs.Bool("admin", false, "any caller's job, with IRGO_REMOTE_ADMIN_TOKEN instead of IRGO_REMOTE_TOKEN: found in the queue's index (jobs that ended in the last day), its files read through the admin route")
	return fs
}

// runRemoteResult downloads a final job's files and exits with its code, so
// a script that submitted with -wait=false gets the same answer later. With
// -admin the job may be another caller's.
func runRemoteResult(v values, args []string) error {
	id, err := oneID(args, "remote result [-o dir] [-admin]")
	if err != nil {
		return err
	}
	admin := v.Bool("admin")
	var c *remote.Client
	if admin {
		if c, err = remote.FromEnv(wire.ScopeJobsAdmin); err != nil {
			return fmt.Errorf("%w: %w", errUsage, err)
		}
	} else if c, err = remoteClient(); err != nil {
		return err
	}
	ctx := context.Background()
	j, err := resultJob(ctx, c, id, admin)
	if err != nil {
		return remoteErr(err)
	}
	if !wire.JobFinal(j.State) {
		return &remote.JobError{ID: id, Code: remote.Code(j), Msg: "still " + j.State + "; wait with: irgo-winvm remote logs -f " + id}
	}
	return fetchAndReport(ctx, c, j, v.String("o"), v.Bool("json"), admin, utmvm.Printer("remote-result"))
}

// resultJob is the job remote-result fetches: the caller's own by its id, or
// with admin any caller's, from the admin token's list, which has no route
// for one job.
func resultJob(ctx context.Context, c *remote.Client, id string, admin bool) (remote.Job, error) {
	if !admin {
		return c.Status(ctx, id)
	}
	jobs, err := c.List(ctx)
	if err != nil {
		return remote.Job{}, err
	}
	for _, j := range jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return remote.Job{}, fmt.Errorf("%w: %s is not in the queue's index, which keeps jobs for a day after they end", remote.ErrNotFound, id)
}

func runRemoteCancel(_ values, args []string) error {
	id, err := oneID(args, "remote cancel")
	if err != nil {
		return err
	}
	c, err := remoteClient()
	if err != nil {
		return err
	}
	j, err := c.Cancel(context.Background(), id)
	if err != nil {
		return remoteErr(err)
	}
	say := utmvm.Printer("remote-cancel")
	if j.State == wire.JobRunning {
		say("job %s is running on %s; it stops at its next step (heartbeats are every 20 s; a program already running finishes or reaches its timeout first) and its VM is deleted", id, j.Runner)
		return nil
	}
	say("job %s: %s", id, j.State)
	return nil
}
