// A probe for every native capability that needs no window, because neither
// repo ships one. Reports OK / UNSUPPORTED / MISSING per capability so the real
// coverage is visible on each platform.
//
// Headless on purpose: this is what can run under the QEMU guest agent, which
// executes as SYSTEM in session 0 where there is no window station at all. The
// windowed half — tray, menus, file dialogs, app icon — is examples/glaze-all,
// which needs `irgo-winvm app-create -gui`.
//
// The split is by what a capability NEEDS, and each capability is claimed by
// exactly one of the two. glaze-all used to probe these four as well, with the
// same canary and the same assertions; two programs answering the same question
// is two places to fix when native changes, and two reports to reconcile when
// they disagree. These four are this file's, and only this file's.
//
// Side-effect policy: the clipboard is saved and restored; nothing opens a
// browser, and no tray icon is left behind.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/crgimenes/native/clipboard"
	"github.com/crgimenes/native/mmap"
	"github.com/crgimenes/native/power"
	"github.com/crgimenes/native/singleinstance"
)

type result struct {
	name, status, detail string
}

var results []result

func record(name string, err error, detail string) {
	switch {
	case err == nil:
		results = append(results, result{name, "OK", detail})
	case errors.Is(err, errors.ErrUnsupported):
		results = append(results, result{name, "UNSUPPORTED", err.Error()})
	default:
		results = append(results, result{name, "ERROR", err.Error()})
	}
}

func probeClipboard() {
	// Save whatever the user had, restore it no matter what happens.
	orig, readErr := clipboard.ReadText()
	if readErr == nil {
		defer func() { _ = clipboard.WriteText(orig) }()
	}

	const canary = "glaze-native-probe-canary"
	if err := clipboard.WriteText(canary); err != nil {
		record("clipboard.write", err, "")
		return
	}
	record("clipboard.write", nil, "")

	got, err := clipboard.ReadText()
	if err != nil {
		record("clipboard.read", err, "")
		return
	}
	if got != canary {
		record("clipboard.read", fmt.Errorf("round trip mismatch: %q", got), "")
		return
	}
	record("clipboard.read", nil, "round trip verified")
}

func probePower() {
	tok, err := power.PreventSleep("glaze native probe")
	if err != nil {
		record("power.preventSleep", err, "")
		return
	}
	tok.Release()
	record("power.preventSleep", nil, "acquired + released")
}

func probeSingleInstance() {
	inst, err := singleinstance.Acquire("glaze-native-probe", singleinstance.Options{})
	if err != nil {
		record("singleinstance.acquire", err, "")
		return
	}
	defer inst.Release()

	// A second acquire must fail while the first is held.
	second, err2 := singleinstance.Acquire("glaze-native-probe", singleinstance.Options{})
	if err2 == nil {
		second.Release()
		record("singleinstance.acquire", errors.New("second acquire unexpectedly succeeded"), "")
		return
	}
	record("singleinstance.acquire", nil, "lock held; re-acquire correctly refused")
}

func probeMmap() {
	f, err := os.CreateTemp("", "glaze-probe-*")
	if err != nil {
		record("mmap.map", err, "")
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if _, err := f.WriteString("0123456789"); err != nil {
		record("mmap.map", err, "")
		return
	}
	m, err := mmap.Map(f)
	if err != nil {
		record("mmap.map", err, "")
		return
	}
	m[0] = 'X'
	detail := fmt.Sprintf("mapped %d bytes, wrote through", len(m))
	_ = m.Unmap()
	record("mmap.map", nil, detail)
}

func main() {
	fmt.Printf("native capability probe — %s/%s\n\n", runtime.GOOS, runtime.GOARCH)

	probeClipboard()
	probePower()
	probeSingleInstance()
	probeMmap()

	// The windowed capabilities — openurl, tray, menu, file dialogs, app icon,
	// no-capture — are covered by examples/glaze-all, which has a run loop and
	// a window and therefore can actually call them.
	//
	// They used to be listed here as SKIPPED rows, which was worse than saying
	// nothing: a report naming a capability reads as coverage of it, and the
	// only thing behind those rows was a `!= nil` on a function value that is
	// never nil. `go vet` says so outright — "comparison of function Open != nil
	// is always true" — so the check was not merely weak, it was no check.

	// Not implemented anywhere in the ecosystem.
	results = append(results,
		result{"notifications", "MISSING", "native/notify is planned (⬜), not built"},
		result{"keychain", "MISSING", "planned (⬜)"},
		result{"fswatch", "MISSING", "planned (⬜)"},
	)

	fmt.Printf("%-26s %-12s %s\n", "CAPABILITY", "STATUS", "DETAIL")
	fmt.Println("-------------------------- ------------ ------------------------------------")
	for _, r := range results {
		fmt.Printf("%-26s %-12s %s\n", r.name, r.status, r.detail)
	}
}
