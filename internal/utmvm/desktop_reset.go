package utmvm

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"
)

// desktopResetScript closes what checks and Windows Update leave on the
// guest's desktop. What it closes, and why each is closed the way it is, is in
// the script.
//
//go:embed assets/desktop-reset.ps1
var desktopResetScript string

// ErrDesktopNotClean is a desktop reset that ran and found something it could
// not close, or no taskbar afterwards.
var ErrDesktopNotClean = errors.New("the VM's desktop is not clean")

// DesktopReset closes stray Explorer windows and error boxes, Windows Update's
// restart prompt and an open Start menu on the guest's desktop, then checks
// that they are gone and the taskbar is still there. It never kills
// explorer.exe.
//
// It runs in user's desktop session, through the same scheduled task as
// app-create -gui: the guest agent runs as SYSTEM in session 0, which can
// neither see nor close a window on the desktop. So, like -gui, it needs
// someone logged in, and says so when nobody is.
func DesktopReset(vmRef, user string, say func(string, ...any)) error {
	return desktop(vmRef, user, false, say)
}

// DesktopCheck is DesktopReset's detection with nothing closed: it reports
// what is on user's desktop and returns ErrDesktopNotClean if anything but
// the shell is, any other window included. For vm-check, which must not
// change the VM it checks.
func DesktopCheck(vmRef, user string, say func(string, ...any)) error {
	return desktop(vmRef, user, true, say)
}

func desktop(vmRef, user string, checkOnly bool, say func(string, ...any)) error {
	if err := requireDesktopSession(vmRef, user, say); err != nil {
		return err
	}
	guest := guestPublic + `\` + scratchPrefix + `desktop-reset.ps1`
	if err := pushScript(vmRef, guest, desktopResetScript); err != nil {
		return fmt.Errorf("pushing the desktop reset: %w", err)
	}
	args := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", guest}
	what := "desktop reset"
	if checkOnly {
		args, what = append(args, "-CheckOnly"), "desktop check"
	}
	res, err := appExecInteractive(vmRef, "powershell", args, user, 2*time.Minute, false, func(string, ...any) {})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	// Scratch: under scratchPrefix, so app-delete sweeps it if this fails.
	_, _ = Named(vmRef).Exec("cmd.exe", "/c", "del /q "+guest)
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			say("desktop: %s", line)
		}
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w: the %s exited %d (the lines above say what is open)", ErrDesktopNotClean, what, res.ExitCode)
	}
	return nil
}
