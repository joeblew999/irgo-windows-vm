package utmvm

import (
	"strings"
	"testing"
)

// TestListRefusedIsAnError: utmctl refused by macOS (pitchfork's supervisor
// before its Automation prompt was answered, 3 Oct 2026) exits 0 with the
// header alone, which read as "UTM has no VMs". It is an error.
//
// Negative control, run by hand: drop the "Error from event" check and the
// refusal is an empty list.
func TestListRefusedIsAnError(t *testing.T) {
	const header = "UUID                                 Status   Name\n"
	refused := "Error from event: The operation couldn’t be completed. (OSStatus error -1743.)\n" +
		"NOTE: utmctl does not work from SSH sessions or before logging in.\n"
	if es, err := listFrom(header, refused, nil); err == nil || !strings.Contains(err.Error(), "-1743") {
		t.Fatalf("refused: %v, %v; want an error naming -1743", es, err)
	}
	es, err := listFrom(header+"5E1DA887-BB0A-41E9-90E3-3D4CE05AD381 started  claude-rig linux\n", "", nil)
	if err != nil || len(es) != 1 || es[0].Name != "claude-rig linux" || es[0].Status != "started" {
		t.Fatalf("a list: %+v, %v", es, err)
	}
}
