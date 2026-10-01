package mcpserver

import (
	"regexp"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
)

// TestInstructionsReachTheClient: a connecting agent is told how the tools fit
// together, in the initialize response.
//
// Negative control, run by hand: pass nil ServerOptions in New and this fails.
func TestInstructionsReachTheClient(t *testing.T) {
	cs := connect(t, nil)
	if got := cs.InitializeResult().Instructions; got != Instructions {
		t.Fatalf("the client was told %q, want Instructions", got)
	}
}

// TestInstructionsNameOnlyRealCommands: every hyphenated name in the
// instructions is a command, an exit status or the project's own name, so a
// renamed command cannot leave the agent told to call one that is gone; and
// each retryable status is named, since that is what an agent must not get
// wrong.
//
// Negative control, run by hand: write "vm-golden-pulls" in Instructions and
// this names it.
func TestInstructionsNameOnlyRealCommands(t *testing.T) {
	known := map[string]bool{"irgo-winvm": true, "irgo-windows-vm": true, "mcp.html": true}
	for _, c := range command.All {
		known[c.Name] = true
	}
	for _, o := range command.Outcomes {
		known[o.Name] = true
		if o.Retryable && !strings.Contains(Instructions, o.Name) {
			t.Errorf("retryable status %s is not named", o.Name)
		}
	}
	for _, w := range regexp.MustCompile(`\b[a-z0-9]+(?:-[a-z0-9]+)+\b`).FindAllString(Instructions, -1) {
		if !known[w] && !strings.HasPrefix(w, "joeblew999") {
			t.Errorf("Instructions name %q, which is no command or status", w)
		}
	}
}
