package utmvm

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The golden image is proven against a real VM (docs/findings.md). These cover the
// parts that fail silently before any VM is involved.

// TestCloneScriptTakesItsArguments: the script is rendered with Sprintf, and a
// verb count that drifts from the call renders "%!(EXTRA string=...)" or
// "%!q(MISSING)" into AppleScript, which fails with a syntax error that names
// neither.
//
// It also checks each system keeps its own system disk, and only the first
// drive on that interface: a Linux VM's seed CD is on the same one.
//
// Negative controls, run by hand 3 Oct 2026: drop the last %q from the asset,
// or the "count of keep" test from it, and this fails.
func TestCloneScriptTakesItsArguments(t *testing.T) {
	for _, g := range guests {
		s := cloneScriptFor("src vm", `dst "vm"`, "52:54:00:AB:CD:EF", 4096, g.diskIface)
		if strings.Contains(s, "%!") {
			t.Fatalf("the clone script and cloneVM disagree about its arguments:\n%s", s)
		}
		for _, want := range []string{`"src vm"`, `"dst \"vm\""`, `"52:54:00:AB:CD:EF"`,
			"if (interface of d) is " + string(g.diskIface) + " and (count of keep) is 0 then",
			"found no " + string(g.diskIface) + " system disk", "duplicate src", "set mem to 4096", "memory:mem"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: rendered clone script has no %s", g.name, want)
			}
		}
	}
}

// TestImportAndEjectScriptsTakeOneArgument, for the same reason.
func TestImportAndEjectScriptsTakeOneArgument(t *testing.T) {
	for name, s := range map[string]string{
		"import": fmt.Sprintf(importScript, "/tmp/x y.utm"),
		"eject":  fmt.Sprintf(ejectScript, "E39605C4-02F5-4085-AD1A-7AD49F5E4D9B"),
	} {
		if strings.Contains(s, "%!") {
			t.Errorf("the %s script and its caller disagree about its arguments:\n%s", name, s)
		}
	}
}

// TestSealStepsAreTheScripts: a step the Go side runs and the script does not
// accept fails in the guest, minutes into a seal: as a ValidateSet error on
// Windows, and as the Linux script's own refusal.
//
// Negative controls, run by hand: rename "trim" in either system's sealSteps
// and this fails; so does moving Linux's "clean" after its "trim".
func TestSealStepsAreTheScripts(t *testing.T) {
	declared := map[string]*regexp.Regexp{
		GuestWindows: regexp.MustCompile(`ValidateSet\(([^)]*)\)`),
		GuestLinux:   regexp.MustCompile(`(?m)^steps='([^']*)'`),
	}
	for _, g := range guests {
		m := declared[g.name].FindStringSubmatch(g.sealScript)
		if m == nil {
			t.Fatalf("%s: the seal script declares no list of steps", g.name)
		}
		accepted := map[string]bool{}
		for _, s := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
			accepted[strings.Trim(strings.TrimSpace(s), `'"`)] = true
		}
		for _, st := range g.sealSteps {
			if !accepted[st.step] {
				t.Errorf("%s: sealSteps runs %q and the script accepts only %v", g.name, st.step, accepted)
			}
		}
		// What frees blocks has to come before the TRIM that would hand them
		// back, and the last step must record what sealing left.
		order := map[string]int{}
		for i, st := range g.sealSteps {
			if _, seen := order[st.step]; !seen {
				order[st.step] = i
			}
		}
		for _, before := range []string{"decrypt", "cleanup", "clean"} {
			if i, ok := order[before]; ok && i > order["trim"] {
				t.Errorf("%s: TRIM must run after %s, or what it frees is not trimmed", g.name, before)
			}
		}
		if g.sealSteps[len(g.sealSteps)-1].step != "facts" {
			t.Errorf("%s: the last step must be facts, so the manifest records the sealed state", g.name)
		}
	}
}

// TestGoldenManifestRoundTrips, and a missing one reads as absent.
func TestGoldenManifestRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if g := Golden(GuestWindows); g.Manifest != nil || g.Present {
		t.Fatalf("an empty machine reports a golden image: %+v", g)
	}
	want := GoldenManifest{Source: "g1", Created: time.Now().UTC().Truncate(time.Second),
		ToolVersion: "test", Windows: "26100.1", WebView2: "1.2.3.4", Allocated: 1, Apparent: 2}
	if err := writeGoldenManifest(windowsGuest, want); err != nil {
		t.Fatal(err)
	}
	g := Golden(GuestWindows)
	if g.Manifest == nil || *g.Manifest != want {
		t.Fatalf("manifest read back as %+v, want %+v", g.Manifest, want)
	}
	if _, err := os.Stat(GoldenManifestPath(GuestWindows) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary manifest was left behind: %v", err)
	}
	// Each system's image has its own manifest: the Linux one is not the
	// Windows one, and writing it leaves the Windows one as it was.
	// Negative control, run by hand: give linuxGuest goldenManifest
	// "golden.json" and this fails.
	if l := Golden(GuestLinux); l.Manifest != nil || l.Name != GoldenLinuxVMName {
		t.Fatalf("the Linux golden image reads as %+v", l)
	}
	lin := GoldenManifest{Source: "l1", OS: GuestLinux, System: "Ubuntu 24.04.3 LTS, kernel 6.8", Created: want.Created}
	if err := writeGoldenManifest(linuxGuest, lin); err != nil {
		t.Fatal(err)
	}
	if l := Golden(GuestLinux); l.Manifest == nil || *l.Manifest != lin {
		t.Fatalf("Linux manifest read back as %+v, want %+v", l.Manifest, lin)
	}
	if g := Golden(GuestWindows); g.Manifest == nil || *g.Manifest != want {
		t.Fatalf("writing the Linux manifest changed the Windows one: %+v", g.Manifest)
	}
}

// TestGoldenNamesSayTheirSystem: a golden image and its verification clone
// have no record, so guestOf must know them by name, or the Linux image would
// be taken for Windows and sealed and booted as one.
//
// Negative control, run by hand: return before the goldenGuest lookup in
// guestOf and the Linux names fail.
func TestGoldenNamesSayTheirSystem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, want := range map[string]string{
		GoldenVMName: GuestWindows, GoldenVMName + "-verify": GuestWindows,
		GoldenLinuxVMName: GuestLinux, "IRGO-GOLDEN-LINUX-verify": GuestLinux,
	} {
		g, err := guestOf(name)
		if err != nil || g.name != want {
			t.Errorf("%s: %q, %v; want %s", name, g.name, err, want)
		}
		if !protectedVM(name) {
			t.Errorf("%s is not protected from vm-reap", name)
		}
	}
	if !IsGoldenImage("irgo-golden-LINUX") || IsGoldenImage(GoldenLinuxVMName+"-verify") {
		t.Error("IsGoldenImage is wrong about the Linux image or its verification clone")
	}
}
