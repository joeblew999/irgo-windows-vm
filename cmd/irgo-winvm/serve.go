package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/remote"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

const serveAbout = `  On the Mac: takes jobs that remote-submit queued at the Worker and runs
  each on a fresh clone of the golden image, one at a time, deleting the clone
  after. It only connects out to the Worker; nothing listens on this Mac.
  Needs IRGO_REMOTE_URL and IRGO_REMOTE_RUNNER_TOKEN, and a golden image
  (vm-golden-create): a job is never run on a VM that was not cloned for it.
`

func serveFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Duration("poll", 3*time.Second, "how often to ask the Worker for a job while the queue is empty")
	fs.Bool("once", false, "take one job, run it, and exit")
	fs.Bool("overcommit", false, "admit each job's clone even when the running VMs' configured memory leaves this Mac too little; they will swap (vm-create -overcommit)")
	host, _ := os.Hostname()
	fs.String("name", strings.Split(host, ".")[0], "this Mac's name in the jobs it runs")
	return fs
}

// runServe is the Mac's side of the remote queue.
func runServe(v values, _ []string) error {
	c, err := remote.FromEnv(remote.EnvRunnerToken)
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	c.Runner = v.String("name")
	say := utmvm.Printer("serve")
	if g := utmvm.Golden(); !g.Present {
		return fmt.Errorf("%w: no golden image at %s; make one with vm-golden-create (or vm-golden-pull) first. "+
			"serve runs every job on a clone of it and never installs Windows for a job", errUsage, utmvm.Home(g.Bundle))
	}
	say("golden image: %s", utmvm.GoldenVMName)
	say("licence: each clone is a running copy of Windows and needs its own licence; this is for your own use, not a public service")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = remote.Serve(ctx, c, macExecutor{say: say, overcommit: v.Bool("overcommit")}, remote.ServeOptions{
		Poll: v.Duration("poll"), Once: v.Bool("once"), Say: say,
	})
	if errors.Is(err, remote.ErrAuth) {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	return err
}

// macExecutor runs a job the only way serve runs one: a VM cloned from the
// golden image for this job alone, the binary run in it by app-create's own
// library call (as glaze-check -windows does), its output and pictures
// collected, and the VM deleted whatever happened. Nothing from the job runs
// on the Mac: the binary is pushed into the guest and executed there.
type macExecutor struct {
	say        func(string, ...any)
	overcommit bool
}

// jobVMPrefix names every VM serve makes, so one left behind by a crash is
// recognisable as a job's and safe to delete.
const jobVMPrefix = "job-"

// guestOut is the directory {out} stands for in the guest.
var guestOut = utmvm.GuestPublicPath("irgo-job-out")

func (e macExecutor) Run(ctx context.Context, j remote.Job, exe string, log io.Writer) (out remote.Outcome) {
	_ = utmvm.Tee(log, func() error {
		out = e.run(ctx, j, exe)
		return nil
	})
	return out
}

func (e macExecutor) run(ctx context.Context, j remote.Job, exe string) remote.Outcome {
	say := utmvm.Printer("serve-job")
	vm := jobVMPrefix + j.ID[:12]
	fail := func(err error, what string) remote.Outcome {
		return remote.Outcome{Code: exitCode(err), Message: what + ": " + err.Error()}
	}
	release, err := utmvm.Acquire(utmvm.VMLock(vm))
	if err != nil {
		return fail(err, "locking "+vm)
	}
	defer release()

	say("vm:     %s, a clone of %s for this job alone", vm, utmvm.GoldenVMName)
	// The shared Mac's admission, as vm-create's: refused with no-room when
	// another VM would leave too little memory or disk, and the clone
	// recorded as the job's caller's, so status and vm-reap see whose it is.
	owner := utmvm.Caller{ID: "remote:" + j.Owner + "/" + j.ID[:12], Source: "remote job"}
	finish, err := utmvm.BeginCreate(vm, owner, false, e.overcommit, say)
	if err != nil {
		return fail(err, "admitting a VM for the job")
	}
	defer finish()
	t0 := time.Now()
	ok, err := utmvm.CloneFromGolden(vm, say)
	// Deleted whatever happened from here on, cancellation included: a
	// clone that failed to boot is still a clone.
	defer e.deleteVM(vm, say)
	switch {
	case err != nil:
		return fail(err, "cloning the golden image")
	case !ok:
		return remote.Outcome{Code: command.CodeNotRun, Message: "this Mac has no golden image; serve never installs Windows for a job"}
	}
	say("clone ready in %s", time.Since(t0).Round(time.Second))
	if ctx.Err() != nil {
		return remote.Outcome{Code: command.CodeNotRun, Message: "stopped before it ran: " + context.Cause(ctx).Error()}
	}
	ent, err := utmvm.Find(vm)
	if err != nil {
		return fail(err, "finding the clone")
	}

	args := guestArgs(j)
	defaults := appCreateFlags()
	user := defaults.Lookup("user").DefValue
	say("binary: %s %s", j.Spec.Name, strings.Join(args, " "))
	res, runErr := utmvm.AppCreate(ent.UUID, exe, utmvm.AppOptions{
		Args: args, GUI: j.Spec.GUI, User: user, Timeout: time.Duration(j.Spec.TimeoutS) * time.Second, Say: say,
	})
	files := map[string][]byte{}
	raw := []byte(res.Stdout)
	if len(raw) > 0 {
		files["stdout.txt"] = bytes.ReplaceAll(raw, []byte{0x16}, nil)
	}
	if j.Spec.Kind == remote.KindTest && len(raw) > 0 {
		if events, err := test2json(raw); err != nil {
			say("test2json: %v (stdout.txt has the raw output)", err)
		} else {
			files["test2json.json"] = events
		}
	}
	// The desktop first, as the program left it, then what it asked for.
	shot := filepath.Join(os.TempDir(), "irgo-"+vm+"-desktop.png")
	if err := utmvm.Screenshot(vm, shot); err != nil {
		say("desktop screenshot: %v", err)
	} else if b, err := os.ReadFile(shot); err == nil {
		files["desktop.png"] = b
		_ = os.Remove(shot)
	}
	for name, b := range pullShots(ent.UUID, raw, say) {
		files[name] = b
	}

	switch {
	case runErr != nil:
		o := fail(runErr, "running "+j.Spec.Name)
		o.Files = files
		return o
	case res.ExitCode != 0:
		return remote.Outcome{Code: command.CodeFailed, Files: files,
			Message: fmt.Sprintf("%s exited %d in the guest", j.Spec.Name, res.ExitCode)}
	}
	return remote.Outcome{Code: command.CodeOK, Files: files, Message: j.Spec.Name + " exited 0 in the guest"}
}

// guestArgs is the command line the binary gets in the guest: {out}
// replaced, and for a test binary test2json framing and a timeout inside the
// job's, so a hang ends in a goroutine dump naming the test.
func guestArgs(j remote.Job) []string {
	var args []string
	if j.Spec.Kind == remote.KindTest {
		args = append(args, "-test.v=test2json")
		if !hasFlag(j.Spec.Args, "-test.timeout") {
			t := time.Duration(j.Spec.TimeoutS)*time.Second - 30*time.Second
			args = append(args, "-test.timeout="+max(t, 30*time.Second).String())
		}
	}
	for _, a := range j.Spec.Args {
		if j.Spec.Kind == remote.KindTest && (a == "-test.v" || strings.HasPrefix(a, "-test.v=")) {
			continue // the framing above is what test2json reads
		}
		args = append(args, strings.ReplaceAll(a, remote.OutToken, guestOut))
	}
	return args
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// maxShots bounds the pictures pulled from one job.
const maxShots = 32

// pullShots fetches every picture shotPaths finds, from {out} in the guest,
// each under a flat result-file name.
func pullShots(uuid string, output []byte, say func(string, ...any)) map[string][]byte {
	out := map[string][]byte{}
	for _, rel := range shotPaths(output) {
		b, err := utmvm.Pull(uuid, guestOut+`\`+strings.ReplaceAll(rel, "/", `\`))
		if err != nil {
			say("screenshot %s not pulled: %v", rel, err)
			continue
		}
		out[shotName(rel)] = b
	}
	return out
}

// shotPaths is every picture the program named in a `screenshot: <path>`
// line, relative to {out}: the conformance suite's convention
// (examples/conformance), which any test can follow. A name that is not a
// plain relative path under {out} is ignored, and at most maxShots are kept.
func shotPaths(output []byte) []string {
	var paths []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(bytes.ReplaceAll(output, []byte{0x16}, nil)))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && len(paths) < maxShots {
		_, rel, ok := strings.Cut(sc.Text(), "screenshot: ")
		rel = strings.TrimSpace(rel)
		switch {
		case !ok, !strings.HasSuffix(rel, ".png"), strings.Contains(rel, ".."), strings.ContainsAny(rel, ":\x00"),
			strings.HasPrefix(rel, "/"), strings.HasPrefix(rel, `\`), seen[rel]:
			continue
		}
		seen[rel] = true
		paths = append(paths, rel)
	}
	return paths
}

// shotName is a picture's result-file name: flat, and only the characters
// the Worker accepts in one ([A-Za-z0-9_.-], at most 100).
func shotName(rel string) string {
	stem := strings.TrimSuffix(rel, ".png")
	b := []byte("shot-")
	for i := 0; i < len(stem); i++ {
		c := stem[i]
		switch {
		case c == '/' || c == '\\':
			b = append(b, '-')
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '_', c == '.', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b[:min(len(b), 96)]) + ".png"
}

// test2json turns -test.v=test2json output into events with Go's own
// converter. The Mac that serves has Go: it builds this tool.
func test2json(raw []byte) ([]byte, error) {
	c := exec.Command("go", "tool", "test2json", "-t")
	c.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return b, nil
}

// deleteVM removes the job's clone and checks it is gone. A clone that
// survives is said loudly: it holds disk and, if running, 8 GiB of RAM.
func (e macExecutor) deleteVM(vm string, say func(string, ...any)) {
	say("deleting %s", vm)
	if _, err := utmvm.Find(vm); errors.Is(err, utmvm.ErrNoVM) {
		say("%s was never registered; nothing to delete", vm)
		return
	}
	if _, err := utmvm.Delete(vm, true, say); err != nil {
		say("DELETING %s FAILED: %v — remove it with: irgo-winvm vm-delete -vm %s -force", vm, err, vm)
		e.say("job VM %s was not deleted: %v", vm, err)
		return
	}
	if _, err := utmvm.Find(vm); !errors.Is(err, utmvm.ErrNoVM) {
		say("%s is still registered after deleting it (%v) — remove it with: irgo-winvm vm-delete -vm %s -force", vm, err, vm)
		e.say("job VM %s is still there after vm-delete", vm)
		return
	}
	if err := utmvm.ForgetVM(vm); err != nil {
		say("forgetting %s's owner record: %v", vm, err)
	}
	say("deleted %s", vm)
}
