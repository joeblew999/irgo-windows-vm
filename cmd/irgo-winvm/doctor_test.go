package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestDoctorJSON is the contract the iso:test cycle reads.
//
// That cycle asks whether the ISO and the .esd are present by row name, and it
// used to read the answer out of the table's fifth whitespace-separated column.
// Now it reads `present` from this JSON, so what must hold is: the names it asks
// for exist, each path is absolute (a script cannot stat "~/..."), and present
// agrees with the filesystem.
//
// HOME is an empty directory, so the media is absent whatever this machine
// holds: an agreement check run only where everything is present cannot tell
// `present` from `true`.
//
// Negative control, run by hand: renaming the .esd entry in utmvm.Externals
// fails the name check; passing utmvm.Home(e.Path) into the row fails the
// absolute-path check; hard-coding present to true fails the agreement check.
func TestDoctorJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := utmvm.Capture(func() error { return runDoctor([]string{"-json"}) })
	if err != nil {
		t.Fatalf("doctor -json: %v", err)
	}
	var rows []doctorRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("doctor -json is not JSON: %v\n%s", err, out)
	}

	byName := map[string]doctorRow{}
	for _, r := range rows {
		byName[r.What] = r
	}
	for _, want := range []string{"Windows 11 ARM64 ISO", "Windows 11 ARM64 .esd"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("no row %q; iso:test asks for it by that name", want)
		}
	}

	for _, r := range rows {
		if r.What == "irgo-winvm" || r.What == "jobs" || r.Path == "" {
			continue
		}
		if strings.HasPrefix(r.Path, "~") {
			t.Errorf("%s: path %q is abbreviated; a script cannot stat it", r.What, r.Path)
		}
		_, sErr := os.Stat(r.Path)
		if r.Present != (sErr == nil) {
			t.Errorf("%s: present=%v but stat(%s) err=%v", r.What, r.Present, r.Path, sErr)
		}
	}
}
