package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestMain keeps every test in this package off the network: doctor, run by
// several of them, would otherwise ask GitHub for UTM's releases.
func TestMain(m *testing.M) {
	checkUTMReleases = func() utmvm.UTMReleaseCheck {
		return utmvm.UTMReleaseCheck{Err: "network disabled in tests"}
	}
	os.Exit(m.Run())
}

// TestUTMReleaseRows: doctor's UTM rows say update available, none, or
// cannot tell, and never "none" when it could not ask.
//
// Negative controls, each run by hand and seen to fail: setting the verdict to
// "none" in the UTMUpdateCannotTell case (offline, unreadable and
// not-installed rows); swapping Releases.Stable for Releases.Pre in the stable
// row (it reads 5.0.6); dropping the !present case of the note (the
// not-installed row no longer says what vm-create installs).
func TestUTMReleaseRows(t *testing.T) {
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	known := func(stable string) utmvm.UTMReleaseCheck {
		return utmvm.UTMReleaseCheck{Known: true, Releases: utmvm.UTMReleases{
			Stable: &utmvm.UTMRelease{Version: stable, Page: "https://x/" + stable, Published: day},
			Pre:    &utmvm.UTMRelease{Version: "5.0.6", Prerelease: true, Page: "https://x/5.0.6", Published: day},
		}}
	}
	inst := func(v string) utmvm.Install { return utmvm.Install{Version: v, Compatible: true} }
	for _, tc := range []struct {
		name                     string
		in                       utmvm.Install
		present                  bool
		c                        utmvm.UTMReleaseCheck
		stable, pre, update, say string
	}{
		{"up to date", inst("4.7.5"), true, known("4.7.5"), "4.7.5", "5.0.6", "none", "latest stable release; no update"},
		{"update", inst("4.7.5"), true, known("4.7.6"), "4.7.6", "5.0.6", "available", "UTM update available: 4.7.6"},
		{"offline", inst("4.7.5"), true, utmvm.UTMReleaseCheck{Err: "no route"}, "cannot tell", "cannot tell", "cannot tell", "Cannot tell whether UTM 4.7.5 is up to date: no route"},
		{"unreadable version", inst("unknown"), true, known("4.7.5"), "4.7.5", "5.0.6", "cannot tell", "not a version number"},
		{"not installed", utmvm.Install{}, false, known("4.7.5"), "4.7.5", "5.0.6", "cannot tell", "vm-create installs the latest stable release, 4.7.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := utmReleaseRows(tc.in, tc.present, tc.c)
			got := map[string]doctorRow{}
			for _, r := range rows {
				got[r.What] = r
			}
			if !tc.present && got["UTM version"].State != "MISSING" {
				t.Errorf("installed row = %q, want MISSING", got["UTM version"].State)
			}
			for what, want := range map[string]string{"UTM latest stable": tc.stable, "UTM latest beta": tc.pre, utmUpdateRow: tc.update} {
				if got[what].State != want {
					t.Errorf("%s = %q, want %q", what, got[what].State, want)
				}
			}
			if note := got[utmUpdateRow].Note; !strings.Contains(note, tc.say) {
				t.Errorf("note %q does not say %q", note, tc.say)
			}
		})
	}
}

// TestDoctorJSON is the contract the iso:test cycle reads: the row names it
// asks for exist, each path is absolute (a script cannot stat "~/..."), and
// present agrees with the filesystem.
//
// HOME is empty so the media is absent: where everything is present, the
// agreement check cannot tell `present` from `true`.
//
// Negative control: renaming the .esd entry in utmvm.Externals
// fails the name check; passing utmvm.Home(e.Path) into the row fails the
// absolute-path check; hard-coding present to true fails the agreement check.
func TestDoctorJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := utmvm.Capture(func() error { return runTool("doctor", []string{"-json"}) })
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
