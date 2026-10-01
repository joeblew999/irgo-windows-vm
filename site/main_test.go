package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/wire"
)

// TestReferenceIsCaptured: the command reference carries what only the
// binary can say. Writing the page from the markdown on disk instead would
// build, and silently omit the whole flag reference.
//
// Negative control (by hand): capturing stdout alone in capture fails this,
// because every command's flag help goes to stderr.
func TestReferenceIsCaptured(t *testing.T) {
	out, err := generate("reference", "..")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Usage of iso-create:",    // from the binary's stderr
		"Usage of app-create:",    // for every command that has flags
		"download from Microsoft", // a usage string iso-create computes at run time
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the reference does not contain %q", want)
		}
	}
}

// TestGlazeLiveAsksTheRoute: the Glaze status page's script fetches the
// glaze-latest route, from wire's table, as a JSON string relative to the site.
//
// Negative control (by hand): asking wire for RouteGlazePost instead fails it.
func TestGlazeLiveAsksTheRoute(t *testing.T) {
	out, err := generate("glaze-live", "..")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := json.Marshal(strings.TrimPrefix(wire.MustFind(wire.RouteGlazeLatest).Path, "/"))
	if !strings.Contains(string(out), "fetch("+string(path)+",") {
		t.Errorf("the script does not fetch %s:\n%s", path, out)
	}
	if strings.Contains(string(out), "LIVE_API") {
		t.Error("the placeholder was not replaced")
	}
}
