package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// TestMain keeps every test in this package off the network: doctor, run by
// several of them, would otherwise ask GitHub for UTM's releases. It also
// keeps their exits out of the log, where report would list them as the
// user's last commands.
func TestMain(m *testing.M) {
	recordExits = false
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

// TestNextSteps: doctor tells a newcomer what to run next, in order, and the
// first step not done is the one marked; nothing in it needs a checkout.
//
// Negative controls, run by hand: drop the golden case from nextSteps (the
// golden machine is told to install for 45 minutes); drop the !s.vm guard on
// the installer step (a machine with a VM is sent to download 4.2 GB).
func TestNextSteps(t *testing.T) {
	for _, tc := range []struct {
		name      string
		s         setup
		next      string   // the line after the → mark
		want, not []string // in the output, or not
	}{
		{"nothing", setup{}, "UTM, the hypervisor",
			[]string{"brew install --cask utm", "iso-create -fetch", "https://brew.sh", "vm-create -install", "Automation", "app-create"},
			[]string{"✓"}},
		{"UTM only", setup{utm: "4.7.5", brew: true}, "the Windows installer",
			[]string{"✓ UTM, the hypervisor (4.7.5)", "about 45 minutes"}, []string{"https://brew.sh"}},
		{"media", setup{utm: "4.7.5", media: true}, "a Windows VM",
			[]string{"✓ the Windows installer", "vm-create -install"}, nil},
		{"golden image", setup{utm: "4.7.5", golden: true}, "a Windows VM",
			[]string{"clones the golden image"}, []string{"iso-create", "45 minutes"}},
		{"private cache", setup{utm: "4.7.5", cache: "https://x.workers.dev"}, "a Windows VM",
			[]string{"pulls the golden image from your private cache (https://x.workers.dev)"}, []string{"iso-create"}},
		{"VM", setup{utm: "4.7.5", media: true, vm: true}, "your program, on Windows",
			[]string{"✓ a Windows VM", "GOOS=windows GOARCH=arm64"}, []string{"Automation"}},
		{"half a cache", setup{utm: "4.7.5", cacheError: errors.New("IRGO_GOLDEN_TOKEN not set")}, "the Windows installer",
			[]string{"half configured", "IRGO_GOLDEN_TOKEN not set"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := nextSteps(tc.s)
			out := strings.Join(lines, "\n")
			var marked []string
			for _, l := range lines {
				if strings.HasPrefix(l, " → ") {
					marked = append(marked, strings.TrimPrefix(l, " → "))
				}
			}
			if len(marked) != 1 || marked[0] != tc.next {
				t.Errorf("marked %q as next, want only %q\n%s", marked, tc.next, out)
			}
			for _, w := range append(tc.want, "claude mcp add irgo-winvm -- irgo-winvm mcp", utmvm.SiteURL) {
				if !strings.Contains(out, w) {
					t.Errorf("does not say %q\n%s", w, out)
				}
			}
			for _, n := range append(tc.not, "docs/", "mise ") {
				if strings.Contains(out, n) {
					t.Errorf("says %q\n%s", n, out)
				}
			}
		})
	}
}
