package drive

import (
	"fmt"
	"os/exec"
	"strings"
)

// Frontmost names the application the user is working in: System Events'
// frontmost process, asked through osascript. A test compares it before and
// after driving an app, because the one thing the driver must never do on a
// desktop someone is using is take it over.
//
// It needs the Automation permission for System Events, granted to the
// terminal or runner; without it osascript fails and so does this.
func Frontmost() (string, error) {
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to get name of first application process whose frontmost is true`).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
