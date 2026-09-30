package utmvm

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Removing what app-create put on a VM.

// nothingMatched reports whether cmd is saying the pattern matched no files.
//
// Not a failure — it is the goal state of an undo, and an undo that fails when
// there is nothing left to undo cannot be run twice. Every delete in this tool
// promises exactly that.
//
// This was the cause of a delete that failed about one run in five and named a
// different target each time. It was never random: a sweep succeeds while there
// is something to sweep and exits 1 the moment there is not, so the first
// delete passed and the second failed on whichever glob had just been emptied.
//
// Both spellings, because cmd uses two and they are not interchangeable: `dir`
// says "File Not Found" and `del` says "Could Not Find". Only the first was
// handled, which also let del's error text through the listing filter and
// printed it as though it were a file that had been found.
func nothingMatched(s string) bool {
	return strings.Contains(s, "File Not Found") || strings.Contains(s, "Could Not Find")
}

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
		return fmt.Errorf("%w: %s, so nothing on it can be removed\n"+
			"  Start it, or run `irgo-winvm vm-create -install` if Windows is not on it yet", ErrNoAgent, vmRef)
	}
	// Both prefixes, both directories. execPrefix files are normally removed by
	// the call that made them; these globs catch the ones an interrupted run
	// left behind, which is the only reason a sweep exists at all.
	targets := []string{
		guestTemp + `\` + scratchPrefix + `*`,
		guestTemp + `\` + execPrefix + `*`,
		guestPublic + `\` + scratchPrefix + `*`,
		guestPublic + `\` + execPrefix + `*`,
	}
	for _, b := range binaries {
		name := path.Base(strings.ReplaceAll(b, `\`, "/"))
		targets = append(targets, guestTemp+`\`+name, guestPublic+`\`+name)
	}
	// One del per target: cmd stops at the first pattern that matches nothing
	// when they are combined, leaving the rest in place.
	var failed []error
	for _, t := range targets {
		// dir first, so what is about to go can be named. utmctl exec always
		// exits 0, so the listing is the only evidence anything was there.
		if out, lErr := vm.Exec("cmd.exe", "/c", "dir /b "+t); lErr == nil && say != nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\r\n") {
				if line != "" && !nothingMatched(line) {
					say("· %s", line)
				}
			}
		}
		// The reason is kept. This recorded only the target, so a delete that
		// failed one run in five said "could not clean C:\Windows\Temp\irgo-*"
		// and nothing else — the same sentence whether the agent had gone away,
		// the file was locked, or cmd had refused the pattern.
		if _, err := vm.Exec("cmd.exe", "/c", "del /q /f "+t); err != nil && !nothingMatched(err.Error()) {
			failed = append(failed, fmt.Errorf("%s: %w", t, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("utmvm: could not clean %s: %w", vmRef, errors.Join(failed...))
	}
	return nil
}
