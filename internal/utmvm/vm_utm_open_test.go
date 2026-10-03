package utmvm

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeApp is a UTM for utmApp: up answers each "is it running" in turn (the
// last answer repeats), lists is how many times the list lacks the VM before
// it has it, and events records every call in order.
type fakeApp struct {
	absent     bool
	up         []bool
	runningErr error
	openErr    error
	lockErr    error
	lists      int
	duringWait func() // run inside every sleep
	events     []string
	said       []string
}

func (f *fakeApp) app() utmApp {
	return utmApp{
		installed: func() bool { return !f.absent },
		running: func() (bool, error) {
			f.events = append(f.events, "running?")
			if f.runningErr != nil {
				return false, f.runningErr
			}
			up := f.up[0]
			if len(f.up) > 1 {
				f.up = f.up[1:]
			}
			return up, nil
		},
		open: func() error {
			f.events = append(f.events, "open")
			return f.openErr
		},
		quit: func() error {
			f.events = append(f.events, "quit")
			return nil
		},
		list: func() ([]Entry, error) {
			f.events = append(f.events, "list")
			if f.lists > 0 {
				f.lists--
				return stopped("irgo-golden"), nil
			}
			return stopped("irgo-golden", "a1"), nil
		},
		lock: func() (func(), error) {
			if f.lockErr != nil {
				return nil, f.lockErr
			}
			f.events = append(f.events, "lock")
			return func() { f.events = append(f.events, "unlock") }, nil
		},
		sleep: func(d time.Duration) {
			f.events = append(f.events, "wait "+d.String())
			if f.duringWait != nil {
				f.duringWait()
			}
		},
		settle: 2 * time.Second,
		say:    func(format string, a ...any) { f.said = append(f.said, fmt.Sprintf(format, a...)) },
	}
}

func (f *fakeApp) did() string { return strings.Join(f.events, ", ") }

// TestARunningUTMIsAskedAtOnce: with UTM running, a request costs the check
// and nothing else: UTM is not opened and nothing is waited for or said.
//
// Negative control, run by hand: make ensureOpen ignore the answer (`if up &&
// false`) and this opens UTM and waits.
func TestARunningUTMIsAskedAtOnce(t *testing.T) {
	f := &fakeApp{up: []bool{true}}
	if err := f.app().ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if got, want := f.did(), "lock, running?, unlock"; got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	if len(f.said) != 0 {
		t.Errorf("said %q for a UTM that was running", f.said)
	}
}

// TestAClosedUTMIsOpenedAndLeftAloneBeforeTheRequest: the defect of 2 Oct
// 2026. vm-create's first request, an AppleScript, went to a UTM that was
// not running and so launched it, and a UTM launched that way never answers
// a start. So UTM is opened first and sent nothing for the settle time,
// under the lock, and the request goes only after; and the progress output
// says so.
//
// The request here is a real command through utmCommand, `touch` of a file
// that must not exist while UTM is being left alone.
//
// Proven against a fake. Against a real UTM it is measured, with the binary
// from before as the control: docs/findings.md.
//
// Negative controls, run by hand: drop `a.sleep(a.settle)` from
// openAndSettle and the order check fails; release the lock before
// openAndSettle (`release()` in place of `defer release()`) and it fails on
// the unlock coming before the open.
func TestAClosedUTMIsOpenedAndLeftAloneBeforeTheRequest(t *testing.T) {
	sent := filepath.Join(t.TempDir(), "sent")
	f := &fakeApp{up: []bool{false, true}}
	f.duringWait = func() {
		if _, err := os.Stat(sent); err == nil {
			f.events = append(f.events, "THE REQUEST WAS SENT DURING THE WAIT")
		}
	}
	real := utm
	utm = f.app()
	defer func() { utm = real }()

	cmd := utmCommand(context.Background(), "touch", sent)
	if got, want := f.did(), "lock, running?, open, wait 2s, running?, unlock"; got != want {
		t.Fatalf("did:  %s\nwant: %s", got, want)
	}
	if _, err := os.Stat(sent); err == nil {
		t.Fatal("the request ran before its caller ran it")
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("the request, after UTM was opened: %v", err)
	}
	if _, err := os.Stat(sent); err != nil {
		t.Fatalf("the request was not sent after UTM was opened: %v", err)
	}
	said := strings.Join(f.said, "\n")
	for _, want := range []string{"UTM is not running", "opening " + AppPath + " in the background", "waiting 2s before asking it anything"} {
		if !strings.Contains(said, want) {
			t.Errorf("the progress output does not say %q:\n%s", want, said)
		}
	}
}

// TestNothingIsSentToAUTMThatCouldNotBeOpened: the check has three answers,
// and only "running" and "opened, and left alone" send the request. Cannot
// tell, a failed open, a UTM that is not running after the wait, and another
// command holding the lock each fail the request with the reason, unsent.
//
// Negative control, run by hand: have utmCommand drop the error (`cmd.Err =
// nil`) and every case sends its request.
func TestNothingIsSentToAUTMThatCouldNotBeOpened(t *testing.T) {
	for name, tc := range map[string]struct {
		f         *fakeApp
		did, want string
	}{
		"cannot tell whether it is running": {
			f:    &fakeApp{runningErr: errors.New("osascript: exit status 1")},
			did:  "lock, running?, unlock",
			want: "whether UTM is running is not known, so nothing was sent to it: osascript: exit status 1",
		},
		"opening it fails": {
			f:    &fakeApp{up: []bool{false}, openErr: errors.New("open -g -a: exit status 1")},
			did:  "lock, running?, open, unlock",
			want: "opening UTM: open -g -a: exit status 1",
		},
		"not running after the wait": {
			f:    &fakeApp{up: []bool{false}},
			did:  "lock, running?, open, wait 2s, running?, unlock",
			want: "UTM was opened and is not running 2s later",
		},
		"another command is opening or restarting it": {
			f:    &fakeApp{up: []bool{true}, lockErr: busy(UTMLaunchLock)},
			did:  "",
			want: "UTM is being opened or restarted by another command",
		},
	} {
		t.Run(name, func(t *testing.T) {
			sent := filepath.Join(t.TempDir(), "sent")
			real := utm
			utm = tc.f.app()
			defer func() { utm = real }()

			err := utmCommand(context.Background(), "touch", sent).Run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the request's error does not say %q: %v", tc.want, err)
			}
			if _, sErr := os.Stat(sent); sErr == nil {
				t.Error("the request was sent")
			}
			if got := tc.f.did(); got != tc.did {
				t.Errorf("did:  %s\nwant: %s", got, tc.did)
			}
		})
	}
	busyErr := utmApp{installed: func() bool { return true },
		lock: func() (func(), error) { return nil, busy(UTMLaunchLock) }}.ensureOpen()
	if !errors.Is(busyErr, ErrMutationInProgress) {
		t.Errorf("a held lock is not reported as busy (exit 6): %v", busyErr)
	}
}

// TestAUTMThatIsNotInstalledIsLeftToTheRequest: with no UTM.app there is
// nothing to open, on a Mac without it and on every other OS, and the request
// fails as it always did, naming utmctl. Nothing is locked or asked.
//
// Negative control, run by hand: drop the installed check and this asks
// whether UTM is running.
func TestAUTMThatIsNotInstalledIsLeftToTheRequest(t *testing.T) {
	f := &fakeApp{absent: true, up: []bool{false}}
	if err := f.app().ensureOpen(); err != nil {
		t.Fatal(err)
	}
	if got := f.did(); got != "" {
		t.Errorf("did %q for a UTM that is not installed", got)
	}
}

// TestRestartLeavesUTMAloneBeforeListing: the restart quits UTM, waits for
// macOS to say it has gone, opens it, and asks for the list only after the
// settle time. It used to poll the list from the moment of `open`, which is
// the request that makes a UTM stop answering starts.
//
// Negative control, run by hand: ask for the list before the wait (an
// `entryFor(a.list, vm)` at the top of openAndSettle) and the order fails.
func TestRestartLeavesUTMAloneBeforeListing(t *testing.T) {
	f := &fakeApp{up: []bool{true, false, true}, lists: 1}
	if err := f.app().restart("a1", f.app().say); err != nil {
		t.Fatal(err)
	}
	want := "quit, running?, wait 500ms, running?, open, wait 2s, running?, list, wait 500ms, list"
	if got := f.did(); got != want {
		t.Errorf("did:  %s\nwant: %s", got, want)
	}
	said := strings.Join(f.said, "\n")
	for _, want := range []string{"UTM has quit", "waiting 2s before asking it anything", "UTM is back and lists a1"} {
		if !strings.Contains(said, want) {
			t.Errorf("the progress output does not say %q:\n%s", want, said)
		}
	}
}

// TestRestartSaysWhenUTMDidNotQuitOrComeBack: a UTM still running after the
// quit, and one that never lists the VM, each end the restart with the reason.
//
// Negative control, run by hand: drop either deadline test in restart and the
// fake is polled until the test times out.
func TestRestartSaysWhenUTMDidNotQuitOrComeBack(t *testing.T) {
	f := &fakeApp{up: []bool{true}}
	err := f.app().restart("a1", f.app().say)
	if err == nil || !strings.Contains(err.Error(), "still running after 30s") {
		t.Errorf("a UTM that did not quit: %v", err)
	}
	if strings.Contains(f.did(), "open") {
		t.Errorf("UTM was opened though it had not quit: %s", f.did())
	}

	f = &fakeApp{up: []bool{false, true}, lists: 1 << 30}
	err = f.app().restart("a1", f.app().say)
	if err == nil || !strings.Contains(err.Error(), "does not list a1") {
		t.Errorf("a UTM that never listed the VM: %v", err)
	}
}

// TestProgressGoesToTheCommandsPrinter: the line about opening UTM is said by
// code that was handed no printer. It reaches the command's Printer when
// there is one, and never stdout otherwise, where it would land in the middle
// of `doctor -json`.
//
// Negative control, run by hand: have progress call printf when there is no
// Printer and the first half fails.
func TestProgressGoesToTheCommandsPrinter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := progressTo.Swap(nil)
	defer progressTo.Store(prev)

	out, _ := Capture(func() error { progress("opening UTM"); return nil })
	if out != "" {
		t.Errorf("with no Printer, progress wrote to the command's output: %q", out)
	}
	out, _ = Capture(func() error {
		_ = Printer("test")
		progress("opening UTM")
		return nil
	})
	if !strings.Contains(out, "s] opening UTM\n") {
		t.Errorf("with a Printer, progress did not go through it: %q", out)
	}
}

// TestEveryRequestToUTMGoesThroughUTMCommand: utmctl and osascript are run in
// one place, utmCommand, so no request can reach a UTM that is closed or
// still launching. utmRunning is the exception by design: it asks macOS, and
// is what utmCommand asks first.
//
// Read from the syntax tree, not grepped: comments name both tools freely.
//
// Negative control, run by hand: have utm.open run "osascript" in place of
// "open" and this names vm_utm_open.go and init.
func TestEveryRequestToUTMGoesThroughUTMCommand(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	reachesUTM := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.BasicLit:
			return x.Value == `"osascript"` || strings.Contains(x.Value, "utmctl")
		case *ast.CallExpr:
			id, ok := x.Fun.(*ast.Ident)
			return ok && id.Name == "utmctlPath"
		}
		return false
	}
	var checked int
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "exec" || !strings.HasPrefix(sel.Sel.Name, "Command") {
					return true
				}
				checked++
				for _, arg := range call.Args {
					if reachesUTM(arg) && fn.Name.Name != "utmRunning" {
						t.Errorf("%s: %s runs utmctl or osascript itself; use utmCommand, which opens UTM first",
							fset.Position(call.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if checked < 5 {
		t.Fatalf("looked at %d exec.Command calls in the package; the walk is not finding them", checked)
	}
}
