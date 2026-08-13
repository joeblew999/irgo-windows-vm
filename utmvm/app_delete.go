package utmvm

import (
	"fmt"
	"path"
	"strings"
)

// Removing what app-create put on a VM.

// runCleanReport is RunClean with a callback naming each thing removed. A
// delete that prints nothing is indistinguishable from one that did nothing.
// AppDelete is RunClean, naming each thing it removes.
func AppDelete(vmRef string, say func(string, ...any), binaries ...string) error {
	vm := Named(vmRef)

	// The guest has to be answering, or there is nothing to clean and nothing
	// that could clean it. Reporting "cleaned" here was a false success: every
	// del silently failed against a VM with no agent and the command said it
	// had worked.
	if !vm.AgentReady() {
		return fmt.Errorf("utmvm: %s is not answering, so nothing on it can be removed\n"+
			"  Start it, or run `irgo-winvm vm-create -install` if Windows is not on it yet", vmRef)
	}
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
		// dir first, so what is about to go can be named. utmctl exec always
		// exits 0, so the listing is the only evidence anything was there.
		if out, lErr := vm.Exec("cmd.exe", "/c", "dir /b "+t); lErr == nil && say != nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\r\n") {
				if line != "" && !strings.Contains(line, "File Not Found") {
					say("· %s", line)
				}
			}
		}
		if _, err := vm.Exec("cmd.exe", "/c", "del /q /f "+t); err != nil {
			failed = append(failed, t)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("utmvm: could not clean %s from %s", strings.Join(failed, ", "), vmRef)
	}
	return nil
}
