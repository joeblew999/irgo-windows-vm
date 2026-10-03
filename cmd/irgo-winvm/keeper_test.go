package main

import (
	"strings"
	"testing"
)

// TestPitchforkDaemonEdit: the keeper's section is added once, replaced in
// place, and removed, and claude-rig's daemon beside it is never touched.
//
// Negative control, run by hand: end daemonSection at the end of the text
// rather than at the next header, and replacing the keeper's section, which
// comes first, deletes claude-rig's.
func TestPitchforkDaemonEdit(t *testing.T) {
	rig := "[daemons.claude-rig]\nrun = 'nu session.nu'\nboot_start = true\n"
	body := "run = '\"/bin/irgo-winvm\" keeper'\nretry = true\n"

	once := upsertDaemon(rig, keeperDaemon, body)
	if !strings.HasPrefix(once, rig) || !strings.HasSuffix(once, "[daemons."+keeperDaemon+"]\n"+body) {
		t.Fatalf("added:\n%s", once)
	}
	if again := upsertDaemon(once, keeperDaemon, body); again != once {
		t.Fatalf("a second upsert changed the file:\n%s", again)
	}

	// The keeper's section first, then claude-rig's: a new body replaces
	// only the keeper's.
	first := "[daemons." + keeperDaemon + "]\nrun = 'old'\n\n" + rig
	next := upsertDaemon(first, keeperDaemon, body)
	if !strings.Contains(next, rig) || strings.Contains(next, "'old'") || strings.Count(next, "[daemons."+keeperDaemon+"]") != 1 {
		t.Fatalf("replaced:\n%s", next)
	}

	if got := removeDaemon(once, keeperDaemon); strings.TrimSpace(got) != strings.TrimSpace(rig) {
		t.Fatalf("removed:\n%q, want claude-rig's alone", got)
	}
	if got := removeDaemon(next, keeperDaemon); !strings.Contains(got, rig) || strings.Contains(got, keeperDaemon) {
		t.Fatalf("removed from the front:\n%q", got)
	}
	if got := removeDaemon(rig, keeperDaemon); got != rig {
		t.Fatal("removing what is not there changed the file")
	}
	if got := removeDaemon(upsertDaemon("", keeperDaemon, body), keeperDaemon); got != "" {
		t.Fatalf("add then remove on an empty file left %q", got)
	}
}
