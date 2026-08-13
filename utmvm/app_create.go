package utmvm

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// Putting a binary on a VM and starting it.

// AppOptions configures AppCreate. The zero value is a headless run with the default
// timeout, which is what almost every caller wants.
type AppOptions struct {
	Args    []string
	GUI     bool // run in the logged-in user's desktop session
	User    string
	Timeout time.Duration
}

// AppCreate pushes a binary into the guest and runs it there.
//
// This is the inner loop the whole tool exists for: build on the Mac, run on
// Windows, read the output back. ONE entry point, because there used to be
// four — RunLocalBinary, RunLocalBinaryInteractive, appExec, RunInteractive
// — differing only in where the binary landed and which session ran it, and a
// caller had to know which of the four to pick.
//
// GUI is the whole distinction. The guest agent runs as NT AUTHORITY\SYSTEM in
// session 0, which has no window station, so anything that opens a window fails
// there. GUI routes through a scheduled task in the logged-in user's session
// instead, which has a desktop.
func AppCreate(vmRef, localPath string, o AppOptions) (AppResult, error) {
	dir := guestTemp
	if o.GUI {
		// Public, not Windows\Temp: the interactive user must be able to execute it.
		dir = guestPublic
	}
	guestPath := dir + `\` + path.Base(strings.ReplaceAll(localPath, `\`, "/"))
	if err := Push(vmRef, localPath, guestPath); err != nil {
		return AppResult{}, err
	}
	if o.GUI {
		return appExecInteractive(vmRef, guestPath, o.Args, o.User, o.Timeout)
	}
	return appExec(vmRef, append([]string{guestPath}, o.Args...), o.Timeout)
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
func appExecInteractive(vmRef, guestExe string, args []string, user string, timeout time.Duration) (AppResult, error) {
	var res AppResult
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
	// No password parameter: /it means interactive-only, which stores none, and
	// supplying one makes schtasks register the task but fail to run it with
	// "ERROR: Element not found" — which reads like a missing session even when
	// the user is logged in. The old signature took a pass and discarded it.
	// user goes through quoteForCmd, which exists in this file for exactly this
	// and was not used here: an account name with a space broke the command, and
	// one containing & or > was injection into a batch file running in the guest.
	//
	// The schtasks line goes through a pushed batch file for the same reason the
	// command itself does: it contains quotes, and cmd.exe applies its own
	// quote-stripping to a quoted string passed through exec, so the line
	// silently fails.
	launcher := "@echo off\r\n" +
		"schtasks /delete /tn " + task + " /f >nul 2>&1\r\n" +
		"schtasks /create /tn " + task + " /tr " + quoteForCmd([]string{"cmd /c " + inner}) +
		" /sc once /st 23:59 /ru " + quoteForCmd([]string{user}) + " /it /f\r\n" +
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
	_, _ = vm.Exec("cmd.exe", "/c", "schtasks /delete /tn "+task+" /f")
	_, _ = vm.Exec("cmd.exe", "/c", "del /q "+inner+" "+outFile+" "+rcFile+" "+launcherGuest)
	return res, nil
}
