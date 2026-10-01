package glazecheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// Options is one check.
type Options struct {
	Root   string
	Target string // TargetMac or TargetWindows

	// Platform is what the record says it ran on — for Windows, which VM or
	// which runner.
	Platform string

	// BuildEnv is added to the environment `go test -c` runs in. The VM run
	// cross-compiles (GOOS=windows GOARCH=arm64 CGO_ENABLED=0); a native run
	// needs nothing.
	BuildEnv []string

	// Run runs the built test binary with args and returns everything it
	// printed, stdout and stderr together, and an error if it could not be run
	// or exited non-zero. Nil means run it natively on this machine.
	Run func(exe string, args []string) (string, error)

	// NotRun reports an error that means the binary never got to run — the
	// guest agent gone, the lock held — rather than that it ran and failed.
	// Such a run is recorded as CANNOT TELL, never as a failure.
	NotRun func(error) bool

	// ResetDesktop, when set, is run before the suite and after it, so the
	// suite starts on a clean desktop and the run leaves one. What it closed
	// afterwards is printed, so a test that left something open is named in
	// the log rather than silently tidied away. For Windows it is
	// utmvm.DesktopReset; the Mac has none, because the tests close what they
	// open and the owner's desktop is not this tool's to tidy.
	ResetDesktop func() error

	// ShotsDir is the directory the binary writes its screenshots into, as
	// the machine it runs on names it, and Fetch reads one back by the name a
	// test logged (<target>/<Test>.png). For the VM run they are a guest path
	// and utmvm.Pull. Both empty means a temporary directory here, read with
	// os.ReadFile.
	ShotsDir string
	Fetch    func(rel string) ([]byte, error)

	Say func(string, ...any)
}

// ErrFailed is a check that ran and found glaze or native broken in a way
// not already known upstream.
var ErrFailed = errors.New("glaze check failed")

// SuiteTimeout bounds one run of the suite, and is passed to the binary as
// -test.timeout so a hang ends in a goroutine dump naming the test, not in
// silence. The whole suite takes seconds; a test waits ten seconds at most
// for any one step.
const SuiteTimeout = 3 * time.Minute

// TestArgs are what the binary is run with. -test.v=test2json frames the
// output for `go tool test2json`, which the host runs; nothing parses the
// human-readable form.
var TestArgs = []string{"-test.v=test2json", "-test.timeout=" + SuiteTimeout.String()}

// Check builds the conformance suite, runs it, records the verdict in
// docs/GLAZE-STATUS.md, and returns the section it wrote.
//
// Everything it prints — its own steps, the build, the suite's raw output —
// also goes to a log file of its own in the tool's log directory, named for
// the target and the time, with the test2json events beside it as .json.
// Both paths are printed first and last. A failure three screens up is
// otherwise gone the moment the terminal scrolls, and over MCP there is no
// terminal at all.
func Check(o Options) (Section, error) {
	start := time.Now()
	say := o.Say
	sec := Section{Target: o.Target, When: start, Platform: o.Platform, RunURL: runURL()}

	if o.ShotsDir == "" {
		dir, err := os.MkdirTemp("", "glaze-shots-")
		if err != nil {
			return sec, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		o.ShotsDir = dir
		o.Fetch = func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) }
	}

	logPath, logFile, err := openLog(o.Target, start)
	if err != nil {
		return sec, err
	}
	jsonPath := strings.TrimSuffix(logPath, ".log") + ".json"
	sec.Log, sec.JSON = utmvm.Home(logPath), utmvm.Home(jsonPath)
	say("full log: %s", sec.Log)

	var notRun, resetErr error
	teeErr := utmvm.Tee(logFile, func() error {
		say("checkout: %s", o.Root)
		say("target:   %s", o.Platform)
		var err error
		if sec.Tree, err = ReadTree(o.Root); err != nil {
			return err
		}
		if sec.Deps, sec.GoWork, err = ReadDeps(o.Root); err != nil {
			return err
		}
		dirty := ""
		if sec.Tree.Dirty {
			dirty = " (with uncommitted changes)"
		}
		say("commit:   %s%s", short(sec.Tree.Commit), dirty)
		for _, d := range sec.Deps {
			say("built against: %s", d)
		}
		if sec.GoWork != "" {
			say("workspace: %s", sec.GoWork)
		}

		exe, bErr := build(o, say)
		if bErr != nil {
			// Recorded, not just returned: "it does not compile against this
			// glaze" is an answer to the question, and the most likely one
			// straight after linking a clone mid-edit.
			sec.BuildError = bErr.Error()
		} else {
			args := append(append([]string{}, TestArgs...), ShotsFlag+o.ShotsDir)
			say("running: %s %s", exe, strings.Join(args, " "))
			var raw []byte
			var runErr error
			resetErr = resetAround(o.ResetDesktop, say, func() { raw, runErr = runSuite(o, exe, args) })
			switch {
			case runErr != nil && o.NotRun != nil && o.NotRun(runErr):
				sec.NotRun, notRun = runErr.Error(), runErr
			default:
				if err := convert(o.Target, raw, jsonPath, &sec, runErr, say); err != nil {
					return err
				}
			}
		}
		sec.Elapsed = time.Since(start)
		// Whatever the outcome, so a run that did not build or did not run
		// leaves no earlier run's pictures beside its verdict.
		if err := collectShots(o.Root, &sec, o.Fetch, say); err != nil {
			return fmt.Errorf("recording the screenshots: %w", err)
		}

		say("%s on %s", sec.Verdict(), o.Platform)
		path, rErr := Record(o.Root, sec)
		if rErr != nil {
			return fmt.Errorf("the check ran but its verdict could not be recorded: %w", rErr)
		}
		say("recorded in %s", path)
		if err := summarise(sec); err != nil {
			say("could not append to the GitHub job summary: %v", err)
		}
		return nil
	})
	// Closed and checked: a log that silently lost its tail to a full disk is
	// the one read after something went wrong.
	if cErr := logFile.Close(); cErr != nil && teeErr == nil {
		teeErr = fmt.Errorf("writing %s: %w", logPath, cErr)
	}
	say("full log: %s", sec.Log)
	say("test2json events: %s", sec.JSON)
	switch {
	case teeErr != nil:
		return sec, teeErr
	case notRun != nil:
		// The underlying error, so the exit code says why: a guest agent that
		// went away is retryable, and a caller should be told so.
		return sec, fmt.Errorf("%s: %w", sec.Verdict(), notRun)
	case !sec.Passed():
		return sec, fmt.Errorf("%w: %s — each test's first message is in %s, everything in %s",
			ErrFailed, sec.Verdict(), StatusFile, sec.Log)
	case resetErr != nil:
		// The verdict stands and is recorded; the desktop is a separate
		// failure, returned so the run does not exit 0 over a mess.
		return sec, fmt.Errorf("%s, but %w", sec.Verdict(), resetErr)
	}
	return sec, nil
}

// resetAround runs run between two desktop resets, when there is a reset.
// Both always run, whatever the first returned: the one after is the one that
// names what a test left open. Its error is returned in preference to the one
// before, for the same reason.
func resetAround(reset func() error, say func(string, ...any), run func()) error {
	if reset == nil {
		run()
		return nil
	}
	say("clearing the desktop before the run")
	before := reset()
	if before != nil {
		say("desktop reset before the run: %v", before)
	}
	run()
	say("clearing the desktop after the run (anything closed here was left open by the run)")
	after := reset()
	if after != nil {
		say("desktop reset after the run: %v", after)
		return after
	}
	return before
}

// convert turns the raw output into test2json events, keeps them beside the
// log, and fills in the section's results.
func convert(target string, raw []byte, jsonPath string, sec *Section, runErr error, say func(string, ...any)) error {
	events, err := toJSON(raw)
	if err != nil {
		return err
	}
	if err := writeFile(jsonPath, events); err != nil {
		return err
	}
	if sec.Results, err = parseEvents(target, events); err != nil {
		return err
	}
	if len(sec.Results) == 0 {
		// The binary printed nothing test2json could attribute to a test: it
		// never got as far as running one. The error and its last words are
		// the answer.
		detail := rawTail(raw, 3)
		if runErr != nil {
			detail = strings.TrimSpace(runErr.Error() + " / " + detail)
		}
		sec.Results = []Result{{Name: "(package)", Outcome: Fail, Detail: detail}}
	}
	for _, r := range sec.Results {
		if !r.Inherited && r.failed() {
			say("%s %s: %s", strings.ToUpper(r.Outcome), r.Name, r.Detail)
		}
		if r.Retried != "" {
			say("RETRIED %s (%s): %s", r.Name, r.Outcome, r.Retried)
		}
	}
	return nil
}

// openLog creates the check's own log, beside the tool's log file.
func openLog(target string, t time.Time) (string, *os.File, error) {
	if err := os.MkdirAll(utmvm.LogDir(), 0o755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(utmvm.LogDir(), fmt.Sprintf("glaze-%s-%s.log", target, t.Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", nil, err
	}
	return path, f, nil
}

func writeFile(path string, b []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close() // already failing
		return err
	}
	return f.Close()
}

// build compiles the suite into one test binary, .bin/mac/conformance.test or
// .bin/win/conformance.test.exe, and returns its path.
//
// For the VM: GOOS=windows GOARCH=arm64 CGO_ENABLED=0 (BuildEnv), stripped
// (-s -w). utmctl file push moves ~0.4 MB/s, so size is time, and Push zips
// anything large. CGO_ENABLED=0 is the claim this project rests on: if the
// suite ever needs a C toolchain to cross-compile, something has taken a cgo
// dependency.
func build(o Options, say func(string, ...any)) (string, error) {
	dir, exe := filepath.Join(o.Root, ".bin", "mac"), "conformance.test"
	if o.Target == TargetWindows {
		dir, exe = filepath.Join(o.Root, ".bin", "win"), "conformance.test.exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, exe)
	// A binary from an earlier run must not stand in for one this build did
	// not write.
	if err := os.Remove(out); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	args := []string{"-C", filepath.Join(o.Root, "examples"), "test", "-c", "-trimpath", "-ldflags=-s -w", "-o", out, SuiteDir}
	say("building: %sgo %s", envPrefix(o.BuildEnv), strings.Join(args, " "))
	c := exec.Command("go", args...)
	c.Env = append(os.Environ(), o.BuildEnv...)
	var stderr bytes.Buffer
	c.Stdout = utmvm.Out
	c.Stderr = &stderr
	err := c.Run()
	if stderr.Len() > 0 {
		_, _ = utmvm.Out.Write(stderr.Bytes())
	}
	if err != nil {
		return "", fmt.Errorf("go test -c: %v\n%s", err, strings.TrimSpace(stderr.String()))
	}
	// Checked, not assumed: `go test -c` on a package with no test files for
	// this GOOS exits 0 and writes nothing.
	if _, err := os.Stat(out); err != nil {
		return "", fmt.Errorf("go test -c reported success and wrote no %s (no tests for this GOOS?): %w", out, err)
	}
	return out, nil
}

func envPrefix(env []string) string {
	if len(env) == 0 {
		return ""
	}
	return strings.Join(env, " ") + " "
}

// runSuite runs the binary through o.Run, or natively, and copies what it printed
// to the log.
func runSuite(o Options, exe string, args []string) ([]byte, error) {
	if o.Run != nil {
		out, err := o.Run(exe, args)
		_, _ = io.WriteString(unframed{utmvm.Out}, out)
		if out != "" && !strings.HasSuffix(out, "\n") {
			_, _ = io.WriteString(utmvm.Out, "\n")
		}
		return []byte(out), err
	}
	return runHere(exe, args)
}

// unframed copies test output to a person with the ^V (0x16) bytes
// -test.v=test2json frames it with taken out. They are for test2json, which
// gets the raw bytes; in a terminal or a log they are noise.
type unframed struct{ w io.Writer }

func (u unframed) Write(p []byte) (int, error) {
	if _, err := u.w.Write(bytes.ReplaceAll(p, []byte{0x16}, nil)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// hostOS are the operating systems the suite runs natively on: the two it
// is built for.
var hostOS = map[string]bool{"darwin": true, "windows": true}

// runHere runs the binary on this machine, output to the log as it comes and
// into the returned buffer.
func runHere(exe string, args []string) ([]byte, error) {
	if !hostOS[runtime.GOOS] {
		return nil, fmt.Errorf("the suite is built for darwin and windows, and this is %s", runtime.GOOS)
	}
	// Past -test.timeout, so the binary's own timeout fires first and names
	// the test that hung.
	ctx, cancel := context.WithTimeout(context.Background(), SuiteTimeout+time.Minute)
	defer cancel()
	var buf bytes.Buffer
	c := exec.CommandContext(ctx, exe, args...)
	c.Stdout = io.MultiWriter(&buf, unframed{utmvm.Out})
	c.Stderr = c.Stdout
	err := c.Run()
	if ctx.Err() != nil {
		return buf.Bytes(), fmt.Errorf("%s hung past its own -test.timeout: killed after %s", filepath.Base(exe), SuiteTimeout+time.Minute)
	}
	return buf.Bytes(), err
}

// runURL is the GitHub Actions run this is part of, or "" outside one: the
// record says where a CI verdict and its pictures came from.
func runURL() string {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_RUN_ID") == "" {
		return ""
	}
	return os.Getenv("GITHUB_SERVER_URL") + "/" + os.Getenv("GITHUB_REPOSITORY") + "/actions/runs/" + os.Getenv("GITHUB_RUN_ID")
}

// summarise appends the section to the GitHub Actions job summary when there
// is one, so a CI run shows the same table the file records.
func summarise(sec Section) error {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(sec.markdown() + "\n"); err != nil {
		_ = f.Close() // already failing
		return err
	}
	return f.Close()
}
