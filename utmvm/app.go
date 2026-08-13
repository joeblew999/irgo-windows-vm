package utmvm

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// guestTemp is where pushed binaries and captured output live in the guest.
// C:\Windows\Temp rather than the user profile: it exists on every install and
// does not depend on which account the agent runs as.
const guestTemp = `C:\Windows\Temp`

// guestPublic is where anything destined for the INTERACTIVE session lives.
//
// C:\Windows\Temp is fine for the agent, which runs as SYSTEM, but a scheduled
// task running as the logged-in user cannot execute from there: the task
// completes with "Last AppResult: 1" and no further explanation. C:\Users\Public
// is readable, writable and executable by any interactive user. Verified by
// running the same batch from both locations — Public produced output, Temp
// did not.
const guestPublic = `C:\Users\Public`

// Push copies a local file into the guest.
//
// utmctl's file push reads the payload from stdin rather than taking a source
// path, so the file is streamed in.
// pushScript writes a batch file, sends it to the guest, and removes the local
// copy.
//
// Everything that runs in the guest goes through a batch file rather than
// straight through exec, and that is not a style choice: cmd.exe applies its own
// quote-stripping to a quoted command line passed through `utmctl exec`, so any
// command containing quotes silently fails. Writing it to a file and running the
// file by path is the only reliable route.
//
// Three call sites did this by hand and each could drop the temp file on a
// different error path.
func pushScript(vmRef, guestPath, script string) error {
	tmp, err := os.CreateTemp("", "irgo-script-*.bat")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // scratch
	if _, err := tmp.WriteString(script); err != nil {
		_ = tmp.Close() // already failing
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return Push(vmRef, tmp.Name(), guestPath)
}

// batchFile builds the CRLF batch text that runs argv, capturing its output and
// exit code to files the host can pull afterwards.
//
// The capture is the point: `utmctl exec` never returns the guest's output and
// always exits 0, so a suite that ran nothing looks exactly like one that
// passed.
func batchFile(argv []string, outFile, rcFile string) string {
	return "@echo off\r\n" +
		quoteForCmd(argv) + " > \"" + outFile + "\" 2>&1\r\n" +
		"echo %ERRORLEVEL% > \"" + rcFile + "\"\r\n"
}

func Push(vmRef, localPath, guestPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only

	cmd := exec.Command(utmctlPath(), "file", "push", vmRef, guestPath)
	cmd.Stdin = f
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pushing %s to %s: %w: %s", localPath, guestPath, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// Pull reads a file out of the guest.
func Pull(vmRef, guestPath string) ([]byte, error) {
	cmd := exec.Command(utmctlPath(), "file", "pull", vmRef, guestPath)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pulling %s: %w: %s", guestPath, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// AppResult is the outcome of running something in the guest.
type AppResult struct {
	Stdout   string
	ExitCode int
}

// appExec executes a command in the guest and returns its output and exit
// code.
//
// Two properties of utmctl exec force this shape:
//
//  1. It does not stream the process's output back, and always exits 0 whatever
//     the guest command did. Treating an empty result as success is how a suite
//     that ran nothing would look like a suite that passed.
//  2. Complex command lines do not survive it. Passing
//     `cmd.exe /c "prog" > "out" 2>&1 & echo %ERRORLEVEL% > "rc"` produced
//     neither file: cmd.exe applies its own quote-stripping rules to a string
//     that already contains quotes, and the whole line silently does nothing.
//
// So the command is written to a batch file, pushed, and run by path. A batch
// file has no quoting ambiguity, and each line is parsed as it executes — which
// also makes %ERRORLEVEL% read the previous command's code rather than being
// expanded early, as it is in a single chained line.
func appExec(vmRef string, argv []string, timeout time.Duration) (AppResult, error) {
	var res AppResult
	if len(argv) == 0 {
		return res, fmt.Errorf("no command given")
	}
	if timeout == 0 {
		timeout = 10 * time.Minute
	}

	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	batFile := guestTemp + `\irgo-` + stamp + `.bat`
	outFile := guestTemp + `\irgo-out-` + stamp + `.txt`
	rcFile := guestTemp + `\irgo-rc-` + stamp + `.txt`

	if err := pushScript(vmRef, batFile, batchFile(argv, outFile, rcFile)); err != nil {
		return res, err
	}
	if _, err := Named(vmRef).Exec("cmd.exe", "/c", batFile); err != nil {
		return res, err
	}

	// exec returns once the process is launched, so wait for the exit-code file
	// rather than assuming the work is done.
	deadline := time.Now().Add(timeout)
	var rcRaw []byte
	for {
		var perr error
		rcRaw, perr = Pull(vmRef, rcFile)
		if perr == nil && len(bytes.TrimSpace(rcRaw)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			return res, fmt.Errorf("guest command did not finish within %s", timeout)
		}
		time.Sleep(2 * time.Second)
	}

	// Both checked. Swallowing these meant a failed pull or an unparsable exit
	// code returned AppResult{Stdout: "", ExitCode: 0} with err == nil — a suite
	// that ran nothing, indistinguishable from a suite that passed. Everything
	// used to verify this VM works runs through here, so it must not lie.
	out, perr := Pull(vmRef, outFile)
	if perr != nil {
		return res, fmt.Errorf("reading command output from %s: %w", vmRef, perr)
	}
	res.Stdout = string(bytes.TrimRight(out, "\r\n"))

	n, cerr := strconv.Atoi(strings.TrimSpace(string(rcRaw)))
	if cerr != nil {
		return res, fmt.Errorf("unreadable exit code %q from %s: %w", string(rcRaw), vmRef, cerr)
	}
	res.ExitCode = n

	_, _ = Named(vmRef).Exec("cmd.exe", "/c", "del /q "+batFile+" "+outFile+" "+rcFile)
	return res, nil
}

// quoteForCmd renders argv for cmd.exe, quoting only what needs it.
//
// Paths under C:\Windows\Temp are space-free, but a caller's arguments are not
// guaranteed to be, and an unquoted space silently becomes two arguments.
func quoteForCmd(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == "" || strings.ContainsAny(a, ` "&|<>^`) {
			parts = append(parts, `"`+strings.ReplaceAll(a, `"`, `""`)+`"`)
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// The answer file and boot script are embedded so the binary is self-contained:
// a developer clones, builds, runs. Shipping them as loose files next to the
// executable is how tools break when moved.
var (
	//go:embed assets/autounattend.xml
	autounattendXML []byte

	//go:embed assets/startup.nsh
	startupNSH []byte

	// The probe runner ships with the tool rather than being supplied by the
	// caller. It was previously expected to appear in the -probes directory,
	// which meant `probe` could never work as shipped: nothing generated it.
	//go:embed assets/run-all.cmd
	runAllCmd []byte
)
