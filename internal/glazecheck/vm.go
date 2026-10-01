package glazecheck

import (
	"fmt"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

// VM is the VM conformance suite, examples/vmconformance: every property of
// the Windows guest this project relies on, checked inside the guest, as
// SYSTEM and in dev's desktop session. Recorded in docs/VM-STATUS.md with a
// section per VM checked — irgo-win11 always, then any other (the golden
// image's verification clone, a disposable clone) once it has been.
var VM = Suite{
	Name:         "vm",
	Command:      "vm-check",
	Dir:          "./vmconformance",
	Package:      repoModule + "/examples/vmconformance",
	Exe:          "vmconformance.test",
	Source:       vmSource,
	StatusFile:   VMStatusFile,
	ShotsDir:     "docs/screens/vm-conformance",
	ShotsFlag:    "-vmconformance.shots=",
	ShotPrefix:   "vm",
	Evidence:     true,
	Targets:      []string{utmvm.DefaultVMName},
	Open:         true,
	Header:       vmHeader,
	Title:        func(vm string) string { return vm },
	Short:        func(vm string) string { return vm },
	Placeholder:  func(vm string) string { return "Run `irgo-winvm vm-check -vm " + vm + "`." },
	Known:        func() []Known { return KnownVM },
	KnownVerdict: "KNOWN ISSUES ONLY",
	KnownTail:    " fail, known VM issues listed in glazecheck.KnownVM; nothing else did",
	KnownLabel:   "known",
	XPassTail:    " — if the VM is fixed, remove it from glazecheck.KnownVM",
	Thing:        "VM conformance suite",
	Subject:      "the VM",
	ShotsBullet:  "- screenshots: %d of %d taken — see [Screenshots](#screenshots)\n",
	GalleryIntro: "Every check with something to see photographs it inside the guest, in dev's session, at the moment that shows " +
		"what it checked — the desktop, a WebView2 window rendering, the setting as Windows reports it in a console — and the host " +
		"photographs the whole VM with `vm-screen` at the end, after looking for stray windows. Every window a check opens it closes. " +
		"A capture that failed says why instead of showing a picture; a black or one-colour frame counts as failed. How each is " +
		"taken is in `examples/vmconformance`.\n\n",
	Marker: func(s Section) []string {
		return []string{
			"commit=" + s.Tree.Commit,
			fmt.Sprintf("source-dirty=%t", s.Tree.ExamplesDirty),
			"when=" + s.When.UTC().Format(time.RFC3339),
		}
	},
	Fresh: func(root string) func(map[string]string) string {
		return func(rec map[string]string) string { return vmFresh(root, rec, time.Now()) }
	},
}

// VMStatusFile is where the VM suite's verdicts are recorded.
const VMStatusFile = "docs/VM-STATUS.md"

// vmSource is what decides whether a VM record still describes the tree.
const vmSource = "examples/vmconformance"

// KnownVM is every VM property known to be missing, with why. Empty: a VM
// that fails a check is reported, and only a decision recorded here — with
// its reason — turns that into KNOWN ISSUES ONLY.
var KnownVM []Known

const vmHeader = `# VM status

Does each Windows VM have every property this project relies on? The last
recorded answer per VM, written by ` + "`irgo-winvm vm-check`" + ` and read back by
` + "`irgo-winvm vm-status`" + `. Generated: do not edit it by hand. Each run replaces
only its own VM's section. Every row is one test of ` + "`examples/vmconformance`" + `,
run inside the guest — as SYSTEM through the guest agent, and in dev's desktop
session (the ` + "`TestSession`" + ` tests) — from its test2json events, or a check only
the host can make (` + "`Host/`" + `). What each checks is in its comment. The checks
only read: none changes the VM.

`

// vmFresh is whether a VM section still describes the tree. Only half the
// question can be answered: whether the checks are what they were. The VM
// itself changes under any record — Windows Update alone sees to that — so it
// is never called current, only how old the answer is.
func vmFresh(root string, rec map[string]string, now time.Time) string {
	commit := rec["commit"]
	if commit == "" {
		return "CANNOT TELL — the section names no commit"
	}
	if rec["source-dirty"] == "true" {
		return "CANNOT TELL — it was recorded with uncommitted changes in examples/vmconformance, which git cannot compare against"
	}
	changed, err := sourceChanged(root, commit, vmSource)
	if err != nil {
		return "CANNOT TELL — " + err.Error()
	}
	if changed {
		return "STALE — examples/vmconformance has changed since " + short(commit)
	}
	age := "at an unrecorded time"
	if when, err := time.Parse(time.RFC3339, rec["when"]); err == nil {
		age = strings.TrimSuffix(now.Sub(when).Round(time.Minute).String(), "0s") + " ago"
	}
	return "checks unchanged since " + short(commit) + "; recorded " + age +
		", and the VM itself can have changed since (run vm-check again for a current answer)"
}

// Changed names the tests that failed in before and pass in after (fixed),
// and those that passed and now fail (broke), in after's order. A parent
// failed only by a subtest is the subtest's failure, not a second one.
func Changed(before, after Section) (fixed, broke []string) {
	was := map[string]Result{}
	for _, r := range before.Results {
		was[r.Name] = r
	}
	for _, r := range after.Results {
		b, ok := was[r.Name]
		if !ok || r.Inherited || b.Inherited {
			continue
		}
		switch {
		case b.failed() && r.Outcome == Pass:
			fixed = append(fixed, r.Name)
		case b.Outcome == Pass && r.failed():
			broke = append(broke, r.Name)
		}
	}
	return fixed, broke
}
