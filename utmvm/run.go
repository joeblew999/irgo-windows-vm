package utmvm

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
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
// completes with "Last Result: 1" and no further explanation. C:\Users\Public
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
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(script); err != nil {
		tmp.Close()
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
	defer f.Close()

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

// Result is the outcome of running something in the guest.
type Result struct {
	Stdout   string
	ExitCode int
}

// RunInGuest executes a command in the guest and returns its output and exit
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
func RunInGuest(vmRef string, argv []string, timeout time.Duration) (Result, error) {
	var res Result
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

	if out, perr := Pull(vmRef, outFile); perr == nil {
		res.Stdout = string(bytes.TrimRight(out, "\r\n"))
	}
	if n, cerr := strconv.Atoi(strings.TrimSpace(string(rcRaw))); cerr == nil {
		res.ExitCode = n
	}

	_, _ = Named(vmRef).Exec("cmd.exe", "/c", "del /q "+batFile+" "+outFile+" "+rcFile)
	return res, nil
}

// EnsureReady brings a VM to a state where commands can be run in it.
//
// A Windows guest reboots on its own — Windows Update did so mid-session here,
// dropping the agent with "Port is not connected" — and UTM's firmware does not
// reliably auto-boot afterwards. So "is the VM ready" cannot be assumed just
// because it was ready a minute ago, and every entry point that talks to the
// guest has to be able to recover rather than fail.
func EnsureReady(vmRef, bundlePath string, timeout time.Duration) error {
	vm := Named(vmRef)
	if vm.AgentReady() {
		return nil
	}
	if st, _ := vm.Status(); st != "started" {
		// Resuming a suspended VM restores RAM and never reaches the firmware,
		// so this is both the fast path and the one needing no keystrokes.
		if err := vm.StartWithDisplay(); err != nil {
			return err
		}
		if err := vm.WaitForAgent(timeout); err == nil {
			return nil
		}
	}
	// Running but unreachable means it is sitting in the UEFI shell after a
	// reboot. Drive it out the same way an install would.
	return RunInstall(InstallOptions{VMRef: vmRef, BundlePath: bundlePath, Timeout: timeout})
}

// RunLocalBinary pushes a binary into the guest and runs it there.
//
// This is the inner loop the whole tool exists for: build on the Mac, run on
// Windows, read the output back — with no GUI, no keystrokes, and no screen.
func RunLocalBinary(vmRef, localPath string, args []string, timeout time.Duration) (Result, error) {
	guestPath := guestTemp + `\` + path.Base(strings.ReplaceAll(localPath, `\`, "/"))
	if err := Push(vmRef, localPath, guestPath); err != nil {
		return Result{}, err
	}
	return RunInGuest(vmRef, append([]string{guestPath}, args...), timeout)
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

// RunInteractive runs a command in the guest's interactive desktop session.
//
// Necessary for anything with a window. The QEMU guest agent runs as
// NT AUTHORITY\SYSTEM in session 0, which has no window station — a GUI app
// launched through it fails at the point it tries to create one. glaze reports
// this as "webview2: environment/controller creation failed", which reads like a
// missing WebView2 runtime and is not: the runtime was present and healthy
// (151.0.4129.78) while the failure persisted.
//
// A scheduled task with /it runs as the logged-in user in their session, which
// has a desktop. The auto-login the answer file configures is what guarantees
// such a session exists.
func RunInteractive(vmRef, guestExe string, args []string, user, pass string, timeout time.Duration) (Result, error) {
	var res Result
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	task := "irgo" + stamp
	inner := guestPublic + `\irgo-i-` + stamp + `.bat`
	outFile := guestPublic + `\irgo-io-` + stamp + `.txt`
	rcFile := guestPublic + `\irgo-ir-` + stamp + `.txt`

	// The task runs this; it captures output and exit code the same way the
	// headless path does.
	inner_script := batchFile(append([]string{guestExe}, args...), outFile, rcFile)
	if err := pushScript(vmRef, inner, inner_script); err != nil {
		return res, err
	}

	vm := Named(vmRef)
	// /it is the whole point: interactive, in the user's session.
	// /it and /rp are contradictory: /it means interactive-only, which stores no
	// password, and supplying one makes schtasks register the task but fail to
	// run it with "ERROR: Element not found" — which reads like a missing
	// session even when the user is logged in.
	_ = pass
	// The schtasks line goes through a pushed batch file for the same reason the
	// command itself does: it contains quotes, and cmd.exe applies its own
	// quote-stripping to a quoted string passed through exec, so the line
	// silently fails.
	launcher := "@echo off\r\n" +
		"schtasks /delete /tn " + task + " /f >nul 2>&1\r\n" +
		"schtasks /create /tn " + task + " /tr \"cmd /c " + inner + "\" /sc once /st 23:59 /ru " + user + " /it /f\r\n" +
		"schtasks /run /tn " + task + "\r\n"
	launcherGuest := guestPublic + `\irgo-l-` + stamp + `.bat`
	if perr := pushScript(vmRef, launcherGuest, launcher); perr != nil {
		return res, perr
	}
	if _, xerr := vm.Exec("cmd.exe", "/c", launcherGuest); xerr != nil {
		return res, fmt.Errorf("launching interactive task: %w", xerr)
	}

	deadline := time.Now().Add(timeout)
	var rcRaw []byte
	for {
		var perr error
		rcRaw, perr = Pull(vmRef, rcFile)
		if perr == nil && len(bytes.TrimSpace(rcRaw)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			_, _ = vm.Exec("cmd.exe", "/c", "schtasks /delete /tn "+task+" /f")
			return res, fmt.Errorf("interactive command did not finish within %s", timeout)
		}
		time.Sleep(3 * time.Second)
	}
	if out, perr := Pull(vmRef, outFile); perr == nil {
		res.Stdout = string(bytes.TrimRight(out, "\r\n"))
	}
	if n, cerr := strconv.Atoi(strings.TrimSpace(string(rcRaw))); cerr == nil {
		res.ExitCode = n
	}
	_, _ = vm.Exec("cmd.exe", "/c", "schtasks /delete /tn "+task+" /f")
	_, _ = vm.Exec("cmd.exe", "/c", "del /q "+inner+" "+outFile+" "+rcFile+" "+launcherGuest)
	return res, nil
}

// RunLocalBinaryInteractive pushes a binary and runs it on the guest's desktop.
func RunLocalBinaryInteractive(vmRef, localPath string, args []string, user, pass string, timeout time.Duration) (Result, error) {
	// Public, not Windows\Temp: the interactive user must be able to execute it.
	guestPath := guestPublic + `\` + path.Base(strings.ReplaceAll(localPath, `\`, "/"))
	if err := Push(vmRef, localPath, guestPath); err != nil {
		return Result{}, err
	}
	return RunInteractive(vmRef, guestPath, args, user, pass, timeout)
}

// RunClean removes everything `run` put on the guest: the binaries it pushed
// and any scratch files left by a run that did not finish.
//
// This is `run`'s undo, and it exists because `run` had none. A pushed binary
// was never deleted at all, and RunInteractive left its scheduled-task launcher
// behind on every single call — so a VM used for testing accumulated one
// irgo-l-*.bat per invocation, forever.
//
// Guest-side deletion only. The VM itself is `vm-delete`.
func RunClean(vmRef string, binaries ...string) error {
	vm := Named(vmRef)
	targets := []string{
		guestTemp + `\irgo-*`,
		guestPublic + `\irgo-*`,
	}
	for _, b := range binaries {
		name := path.Base(strings.ReplaceAll(b, `\`, "/"))
		targets = append(targets, guestTemp+`\`+name, guestPublic+`\`+name)
	}
	// One del per target: cmd stops at the first pattern that matches nothing
	// when they are combined, leaving the rest in place.
	var failed []string
	for _, t := range targets {
		if _, err := vm.Exec("cmd.exe", "/c", "del /q /f "+t); err != nil {
			failed = append(failed, t)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("utmvm: could not clean %s from %s", strings.Join(failed, ", "), vmRef)
	}
	return nil
}
