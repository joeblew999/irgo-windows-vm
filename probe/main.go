// A single program exercising every native capability available to a glaze
// desktop app, because neither repo ships one. Reports OK / UNSUPPORTED /
// MISSING per capability so the real coverage is visible on each platform.
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
	"github.com/crgimenes/native/openurl"
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

	// Deliberately not executed: side effects the user did not ask for.
	results = append(results,
		result{"openurl.Open", "SKIPPED", "would launch a browser (API present: " + fmt.Sprint(openurl.Open != nil) + ")"},
		result{"tray.Run", "SKIPPED", "would add a status-bar icon; needs a run loop"},
		result{"filedialog", "SKIPPED", "modal, needs a click; use glaze's dialogs (all 3 OSes)"},
		result{"menu.Set", "SKIPPED", "needs a native run loop"},
	)

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
