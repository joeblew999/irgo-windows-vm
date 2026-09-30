package glazecheck

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// Program is one of the four, and how it has to be run.
type Program struct {
	Name string

	// GUI is a program that opens a window, so on Windows it needs app-create
	// -gui: the guest agent runs in session 0, which has no desktop, and glaze
	// reports that as a missing WebView2 runtime. See docs/DEVELOPMENT.md.
	GUI bool

	// Args go to the program. glaze-all opens its window and waits by default,
	// because it is an example before it is a test; -probe is the unattended
	// report.
	Args []string
}

// Programs are the four, in the order they run. All four run even when one
// fails, so one run shows everything that is broken.
var Programs = []Program{
	{Name: "probe"},
	{Name: "verify", GUI: true},
	{Name: "verify-events", GUI: true},
	{Name: "glaze-all", GUI: true, Args: []string{"-probe"}},
}

// Options is one check.
type Options struct {
	Root   string
	Target string // TargetMac or TargetWindows

	// Platform is what the record says it ran on — for Windows, which VM.
	Platform string

	// Run runs one built program and returns nil only if it passed. Its output
	// goes to utmvm.Out, which is where the log and the first-failure line are
	// read from. Nil means run it natively on this machine.
	Run func(p Program, exe string) error

	// NotRun reports an error that means the program never got to run — the
	// guest agent gone, the lock held — rather than that it failed. Such a
	// result is recorded as NOT RUN, never as FAIL.
	NotRun func(error) bool

	Say func(string, ...any)
}

// ErrFailed is a check that ran and found glaze or native broken.
var ErrFailed = errors.New("glaze check failed")

// macTimeout bounds each program on the Mac. glaze-all -probe finishes in a
// few seconds; a program that has not finished in two minutes has hung, and a
// hang is a failure worth recording rather than a wait with no end.
const macTimeout = 2 * time.Minute

// Check builds the four programs, runs them, records the verdict in
// docs/GLAZE-STATUS.md, and returns the section it wrote.
//
// Everything it prints — its own steps, the build, every program's output —
// also goes to a log file of its own in the tool's log directory, named for the
// target and the time, and that path is printed first and last. A failure
// three screens up is otherwise gone the moment the terminal scrolls, and over
// MCP there is no terminal at all.
func Check(o Options) (Section, error) {
	start := time.Now()
	say := o.Say
	sec := Section{Target: o.Target, When: start, Platform: o.Platform}

	logPath, logFile, err := openLog(o.Target, start)
	if err != nil {
		return sec, err
	}
	sec.Log = utmvm.Home(logPath)
	say("full log: %s", sec.Log)

	var failed bool
	var notRun error
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

		out, bErr := build(o.Root, o.Target, say)
		if bErr != nil {
			// Recorded, not just returned: "it does not compile against this
			// glaze" is an answer to the question, and the most likely one
			// straight after linking a clone mid-edit.
			sec.BuildError = bErr.Error()
			failed = true
		}

		for _, p := range Programs {
			if bErr != nil {
				break
			}
			exe := filepath.Join(out, p.Name)
			if o.Target == TargetWindows {
				exe += ".exe"
			}
			say("=== %s", strings.TrimSpace(p.Name+" "+strings.Join(p.Args, " ")))

			var seen bytes.Buffer
			runErr := utmvm.Tee(&seen, func() error {
				if o.Run != nil {
					return o.Run(p, exe)
				}
				return runHere(p, exe)
			})
			r := Result{Name: p.Name, Pass: runErr == nil}
			if runErr != nil {
				r.FirstFail = firstFail(seen.String(), runErr)
				if o.NotRun != nil && o.NotRun(runErr) {
					r.NotRun = true
					notRun = runErr
				} else {
					failed = true
				}
			}
			sec.Results = append(sec.Results, r)
		}
		sec.Elapsed = time.Since(start)

		say("%s on %s", sec.Verdict(), o.Platform)
		path, rErr := Record(o.Root, sec)
		if rErr != nil {
			return fmt.Errorf("the check ran but its verdict could not be recorded: %w", rErr)
		}
		say("recorded in %s", path)
		return nil
	})
	// Closed and checked: a log that silently lost its tail to a full disk is
	// the one read after something went wrong.
	if cErr := logFile.Close(); cErr != nil && teeErr == nil {
		teeErr = fmt.Errorf("writing %s: %w", logPath, cErr)
	}
	say("full log: %s", sec.Log)
	switch {
	case teeErr != nil:
		return sec, teeErr
	case failed:
		return sec, fmt.Errorf("%w: %s — the first failure of each is in %s, everything in %s",
			ErrFailed, sec.Verdict(), StatusFile, sec.Log)
	case notRun != nil:
		// The underlying error, so the exit code says why: a guest agent that
		// went away is retryable, and a caller should be told so.
		return sec, fmt.Errorf("%s: %w", sec.Verdict(), notRun)
	}
	return sec, nil
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

// build compiles the four into .bin/mac or .bin/win and returns the directory.
//
// For Windows: GOOS=windows GOARCH=arm64 CGO_ENABLED=0, stripped (-s -w). utmctl
// file push moves ~0.4 MB/s, so size is time — stripped and zipped (Push zips
// anything large), verify.exe is 2.0 MB, not 6.9. CGO_ENABLED=0 is the claim
// this project rests on: if the examples ever need a C toolchain to
// cross-compile, something has taken a cgo dependency.
func build(root, target string, say func(string, ...any)) (string, error) {
	out := filepath.Join(root, ".bin", "mac")
	args := []string{"-C", filepath.Join(root, "examples"), "build"}
	env := os.Environ()
	if target == TargetWindows {
		out = filepath.Join(root, ".bin", "win")
		args = append(args, "-trimpath", "-ldflags=-s -w")
		env = append(env, "GOOS=windows", "GOARCH=arm64", "CGO_ENABLED=0")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	args = append(args, "-o", out+string(filepath.Separator), "./...")
	say("building: go %s", strings.Join(args, " "))
	c := exec.Command("go", args...)
	c.Env = env
	var stderr bytes.Buffer
	c.Stdout = utmvm.Out
	c.Stderr = &stderr
	err := c.Run()
	if stderr.Len() > 0 {
		_, _ = utmvm.Out.Write(stderr.Bytes())
	}
	if err != nil {
		return "", fmt.Errorf("go build: %v\n%s", err, strings.TrimSpace(stderr.String()))
	}
	// Checked, not assumed: a build that exits 0 and leaves no binary would
	// otherwise be reported as four programs that failed to start.
	for _, p := range Programs {
		exe := filepath.Join(out, p.Name)
		if target == TargetWindows {
			exe += ".exe"
		}
		if _, err := os.Stat(exe); err != nil {
			return "", fmt.Errorf("go build reported success and wrote no %s: %w", exe, err)
		}
	}
	return out, nil
}

// runHere runs a program natively, with its output going to utmvm.Out.
func runHere(p Program, exe string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the host check runs the examples natively and only macOS is a host glaze-check supports; this is %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(context.Background(), macTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, exe, p.Args...)
	c.Stdout, c.Stderr = utmvm.Out, utmvm.Out
	err := c.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("%s hung: killed after %s", p.Name, macTimeout)
	}
	return err
}

// failWord is how the four say something broke. probe marks a row ERROR,
// glaze-all marks one FAILED, verify and verify-events print "FAIL: ...".
// Uppercase and whole-word, so a capability named "failover" or a detail
// saying "failed to" in passing does not count.
var failWord = regexp.MustCompile(`\b(FAIL|FAILED|ERROR)\b`)

// elapsedPrefix is what utmvm.Printer puts in front of a line: app-create
// prints the guest's output through it.
var elapsedPrefix = regexp.MustCompile(`^\[\s*[0-9.]+s\]\s*`)

// firstFail is the line a person reading the record wants: the first one that
// says what broke. When the program printed no such line — it crashed, or
// never started — the error that ended it is the next best answer.
func firstFail(out string, err error) string {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(elapsedPrefix.ReplaceAllString(sc.Text(), ""))
		if failWord.MatchString(line) {
			return line
		}
	}
	return err.Error()
}
