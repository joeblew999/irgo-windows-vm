package utmvm

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
)

// vmRepairScript is the repair itself, in PowerShell. It is a file rather than
// a command line because cmd.exe strips quotes from anything passed through
// `utmctl exec` (see pushScript), and a script with a registry path, braces and
// a pipeline would not survive that.
//
//go:embed assets/vm-repair.ps1
var vmRepairScript string

// VMRepair fixes the two things that silently break -gui runs on a VM that has
// lived a while: an expired password (AutoLogon stops, no desktop session) and
// a stale WebView2 registration (every webview reports the runtime missing).
//
// It runs as SYSTEM through the guest agent, so it works exactly when it is
// needed: nobody can log in, but the agent still answers. With reboot, the VM
// restarts afterwards so AutoLogon runs again.
func VMRepair(vmRef, user string, reboot bool, say func(string, ...any)) error {
	guest := guestPublic + `\irgo-vm-repair.ps1`
	if err := pushScript(vmRef, guest, vmRepairScript); err != nil {
		return fmt.Errorf("pushing the repair script: %w", err)
	}
	res, err := appExec(vmRef, []string{
		"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", guest, "-User", user,
	}, 5*time.Minute, say)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			say("%s", line)
		}
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("repair script exited %d in the guest", res.ExitCode)
	}
	if reboot {
		say("rebooting so AutoLogon runs again")
		if _, err := appExec(vmRef, []string{"shutdown", "/r", "/t", "5"}, time.Minute, say); err != nil {
			return fmt.Errorf("requesting the reboot: %w", err)
		}
	}
	return nil
}

// ErrNoDesktopSession is returned before a -gui launch when nobody is logged in.
//
// Without it the launch goes through a scheduled task that never runs, no
// exit-code file is written, and the caller waits the whole timeout — ten
// minutes that look exactly like a slow program.
var ErrNoDesktopSession = errors.New("no desktop session")

// requireDesktopSession fails fast when user has no interactive session.
func requireDesktopSession(vmRef, user string, say func(string, ...any)) error {
	res, err := appExec(vmRef, []string{
		"tasklist", "/v", "/fi", "imagename eq explorer.exe", "/fo", "csv", "/nh",
	}, time.Minute, say)
	if err != nil {
		return err
	}
	if hasDesktopSession(res.Stdout, user) {
		return nil
	}
	return fmt.Errorf("%w for %s: AutoLogon did not reach the desktop (an expired password or a sign-in screen). "+
		"See it with `irgo-winvm vm-screen`; fix it with `irgo-winvm vm-repair -reboot`", ErrNoDesktopSession, user)
}

// hasDesktopSession reports whether tasklist /v /fo csv output shows an
// explorer.exe owned by user. The "User Name" column is DOMAIN\user, quoted.
func hasDesktopSession(tasklistCSV, user string) bool {
	want := `\` + strings.ToLower(user) + `"`
	for _, line := range strings.Split(strings.ToLower(tasklistCSV), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), `"explorer.exe"`) && strings.Contains(line, want) {
			return true
		}
	}
	return false
}
