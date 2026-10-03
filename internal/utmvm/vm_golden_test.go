package utmvm

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The golden image is proven against a real VM (RESULTS.md). These cover the
// parts that fail silently before any VM is involved.

// TestCloneScriptTakesItsArguments: the script is rendered with Sprintf, and a
// verb count that drifts from the call renders "%!(EXTRA string=...)" or
// "%!q(MISSING)" into AppleScript, which fails with a syntax error that names
// neither.
//
// Negative control, run by hand: drop the last %q from the asset and this fails.
func TestCloneScriptTakesItsArguments(t *testing.T) {
	s := fmt.Sprintf(cloneScript, "src vm", 4096, `dst "vm"`, "52:54:00:AB:CD:EF", `dst "vm"`)
	if strings.Contains(s, "%!") {
		t.Fatalf("the clone script and cloneVM disagree about its arguments:\n%s", s)
	}
	for _, want := range []string{`"src vm"`, `"dst \"vm\""`, `"52:54:00:AB:CD:EF"`, "is NVMe", "duplicate src", "set mem to 4096", "memory:mem"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered clone script has no %s", want)
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
// accept fails in the guest, forty minutes into a seal, as a ValidateSet error.
//
// Negative controls, run by hand: rename "trim" in sealSteps and this fails;
// so does moving "openssh" after "cleanup" (3 Oct 2026).
func TestSealStepsAreTheScripts(t *testing.T) {
	m := regexp.MustCompile(`ValidateSet\(([^)]*)\)`).FindStringSubmatch(sealScript)
	if m == nil {
		t.Fatal("the seal script declares no ValidateSet for -Step")
	}
	accepted := map[string]bool{}
	for _, s := range strings.Split(m[1], ",") {
		accepted[strings.Trim(strings.TrimSpace(s), `'"`)] = true
	}
	for _, st := range sealSteps {
		if !accepted[st.step] {
			t.Errorf("sealSteps runs %q and the script accepts only %v", st.step, accepted)
		}
	}
	// Decrypting has to come before the TRIM that would free what it rewrote,
	// and the last step must record what sealing left.
	order := map[string]int{}
	for i, st := range sealSteps {
		if _, seen := order[st.step]; !seen {
			order[st.step] = i
		}
	}
	if order["decrypt"] > order["trim"] || order["cleanup"] > order["trim"] {
		t.Error("TRIM must run after decrypt and cleanup, or what they free is not trimmed")
	}
	// The component cleanup removes what installing the capability supersedes.
	if _, ok := order["openssh"]; !ok || order["openssh"] > order["cleanup"] {
		t.Error("openssh must run, and before cleanup")
	}
	if sealSteps[len(sealSteps)-1].step != "facts" {
		t.Error("the last step must be facts, so the manifest records the sealed state")
	}
}

// TestGoldenManifestRoundTrips, and a missing one reads as absent.
func TestGoldenManifestRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if g := Golden(); g.Manifest != nil || g.Present {
		t.Fatalf("an empty machine reports a golden image: %+v", g)
	}
	want := GoldenManifest{Source: "g1", Created: time.Now().UTC().Truncate(time.Second),
		ToolVersion: "test", Windows: "26100.1", WebView2: "1.2.3.4", Allocated: 1, Apparent: 2}
	if err := writeGoldenManifest(want); err != nil {
		t.Fatal(err)
	}
	g := Golden()
	if g.Manifest == nil || *g.Manifest != want {
		t.Fatalf("manifest read back as %+v, want %+v", g.Manifest, want)
	}
	if _, err := os.Stat(GoldenManifestPath() + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary manifest was left behind: %v", err)
	}
}
