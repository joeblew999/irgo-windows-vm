package drive

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Frontmost is the application the user is working in: System Events'
// frontmost process, asked through osascript. A test compares it before and
// after driving an app, because the one thing the driver must never do on a
// desktop someone is using is take it over.
//
// It needs the Automation permission for System Events, granted to the
// terminal or runner; without it osascript fails and so does this.
func Frontmost() (App, error) {
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to tell (first application process whose frontmost is true) to return (unix id as text) & tab & name`).CombinedOutput()
	if err != nil {
		return App{}, fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	pid, name, ok := strings.Cut(strings.TrimSpace(string(out)), "\t")
	n, convErr := strconv.Atoi(pid)
	if !ok || convErr != nil {
		return App{}, fmt.Errorf("osascript answered %q, not a pid and a name", out)
	}
	return App{Name: name, PID: n}, nil
}

// stepForeground is not asked on macOS (asked is false): Frontmost runs
// osascript, too slow for every step, and the tests ask it before and after.
func stepForeground() (App, bool, error) { return App{}, false, nil }
