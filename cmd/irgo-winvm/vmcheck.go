package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/glazecheck"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func vmCheckFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-check", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "the VM to check")
	return fs
}

// runVMCheck builds examples/vmconformance, runs it in the VM — as SYSTEM and
// in dev's desktop session — and records every check in docs/VM-STATUS.md.
// The VM's lock is taken by runTool, from -vm.
func runVMCheck(v values, _ []string) error {
	say := utmvm.Printer("vm-check")
	root, err := repo()
	if err != nil {
		return err
	}
	if err := glazecheck.NeedGo(); err != nil {
		return err
	}
	// Resolved before building, so a missing VM fails fast with exit 3 and is
	// never recorded as a verdict.
	e, err := utmvm.Find(v.String("vm"))
	if err != nil {
		return err
	}
	_, err = checkVM(root, e, say)
	return err
}

// The host-side results, named like the suite's tests.
const (
	hostAgent   = "Host/AgentAnswers"
	hostDesktop = "Host/DesktopClean"
)

// checkVM runs the VM suite in e and records the verdict. The caller holds
// e's lock. vm-check, vm-repair -check and vm-golden-create's verification
// all come here, so there is one way to check a VM.
//
// Nothing here changes the VM beyond what running any binary in it does —
// the binary is pushed and removed by app-create's own path — and nothing
// tidies its desktop: the desktop is checked, not reset.
func checkVM(root string, e utmvm.Entry, say func(string, ...any)) (glazecheck.Section, error) {
	// app-create's own defaults, read from its flags, so there is one answer
	// to "which guest account" and "how long".
	defaults := appCreateFlags()
	user := defaults.Lookup("user").DefValue
	timeout := values{defaults}.Duration("timeout")

	// Pictures taken on the host, by the name a result gives them; the rest
	// are the guest's, pulled by the name a test logged.
	local := map[string]string{}
	guestShots := utmvm.GuestPublicPath("irgo-vm-check-shots")

	run := func(gui bool) func(string, []string) (string, error) {
		return func(exe string, args []string) (string, error) {
			res, err := utmvm.AppCreate(e.UUID, exe, utmvm.AppOptions{Args: args, GUI: gui, User: user, Timeout: timeout, Say: say})
			if err != nil {
				return res.Stdout, err
			}
			if res.ExitCode != 0 {
				return res.Stdout, fmt.Errorf("%s exited %d in the guest", filepath.Base(exe), res.ExitCode)
			}
			return res.Stdout, nil
		}
	}

	o := glazecheck.Options{
		Suite:    &glazecheck.VM,
		Root:     root,
		Target:   e.Name,
		Platform: "windows/arm64, VM " + e.Name + " (through app-create: as SYSTEM, and -gui in " + user + "'s session)",
		BuildEnv: []string{"GOOS=windows", "GOARCH=arm64", "CGO_ENABLED=0"},
		Say:      say,
		ShotsDir: guestShots,
		Fetch: func(rel string) ([]byte, error) {
			if p, ok := local[rel]; ok {
				return os.ReadFile(p)
			}
			return utmvm.Pull(e.UUID, guestShots+`\`+strings.ReplaceAll(rel, "/", `\`))
		},
		Before: func() ([]glazecheck.Result, error) { return agentAnswers(e, say) },
		Parts: []glazecheck.Part{
			{Name: "as SYSTEM", Args: []string{"-test.skip=^TestSession"}, Run: run(false)},
			{Name: "session", Args: []string{"-test.run=^TestSession"}, Run: run(true)},
		},
		NotRun: func(err error) bool {
			return errors.Is(err, utmvm.ErrNoAgent) || errors.Is(err, utmvm.ErrNoVM) ||
				errors.Is(err, utmvm.ErrMutationInProgress)
		},
		After: func() []glazecheck.Result { return []glazecheck.Result{desktopClean(e, user, local, say)} },
	}
	return glazecheck.Check(o)
}

// agentAnswers is the host's own check: the guest agent answers. A VM that is
// running and silent fails it, and is then recovered the way every command
// recovers one, so the suite can still run; one that cannot be recovered is
// CANNOT TELL.
func agentAnswers(e utmvm.Entry, say func(string, ...any)) ([]glazecheck.Result, error) {
	r := glazecheck.Result{Name: hostAgent, Outcome: glazecheck.Pass}
	vm := utmvm.Named(e.UUID)
	if vm.AgentReady() {
		r.Evidence = "the guest agent answered"
		return []glazecheck.Result{r}, nil
	}
	status, _ := vm.Status()
	t0 := time.Now()
	if err := ensureAgent(e, say); err != nil {
		return nil, err
	}
	took := time.Since(t0).Round(time.Second)
	if strings.Contains(status, "started") {
		r.Outcome = glazecheck.Fail
		r.Detail = fmt.Sprintf("the VM was running and its guest agent did not answer; it answered %s after recovering it", took)
	}
	r.Evidence = fmt.Sprintf("the VM was %s; the agent answered %s after it was recovered", strings.TrimSpace(status), took)
	return []glazecheck.Result{r}, nil
}

// desktopClean is the host's last check: nothing but the shell is on the
// desktop once the suite has closed what it opened — desktop-reset's own
// detection, closing nothing — and a picture of the whole VM from the host.
func desktopClean(e utmvm.Entry, user string, local map[string]string, say func(string, ...any)) glazecheck.Result {
	r := glazecheck.Result{Name: hostDesktop, Outcome: glazecheck.Pass}
	var seen []string
	err := utmvm.DesktopCheck(e.UUID, user, func(f string, a ...any) {
		seen = append(seen, strings.TrimPrefix(fmt.Sprintf(f, a...), "desktop: "))
		say(f, a...)
	})
	r.Evidence = strings.Join(seen, " / ")
	if err != nil {
		r.Outcome, r.Detail = glazecheck.Fail, err.Error()
	}
	rel := "vm/" + strings.ReplaceAll(hostDesktop, "/", "_") + ".png"
	if p, sErr := utmvm.Shot(e.UUID, "vm-check"); sErr != nil {
		r.NoShot = "vm-screen: " + sErr.Error()
	} else {
		local[rel] = p
		r.Shot, r.ShotNote = rel, "the whole VM, photographed from the host with vm-screen after the check"
	}
	return r
}

// vmStatusFlags is none, so -h is answered whether or not anything has been
// recorded.
func vmStatusFlags() *flag.FlagSet { return flag.NewFlagSet("vm-status", flag.ContinueOnError) }

// runVMStatus prints the recorded VM verdicts whole, then how far each can
// still be trusted.
func runVMStatus(values, []string) error {
	root, err := repo()
	if err != nil {
		return err
	}
	body, err := glazecheck.VM.Read(root)
	if err != nil {
		return err
	}
	say := utmvm.Reporter("vm-status")
	_, _ = fmt.Fprint(utmvm.Out, body)
	say("")
	say("--- from %s, checked against the tree now:", filepath.Join(root, glazecheck.VMStatusFile))
	for _, l := range glazecheck.VM.Freshness(root, body) {
		say("%s", l)
	}
	return nil
}
