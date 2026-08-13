package utmvm

// Seeing the guest's screen.
//
// The one thing that answers "what is it actually doing" when a VM is stuck.
// A boot that fails leaves a UEFI prompt nobody sees, and an install that
// stalls looks identical to one that is working — from the host there is no
// difference until you look.

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//
//go:embed assets/windowid.swift
var windowIDSwift string

// Screenshot captures a VM's display window to a PNG.
//
// This is the only reliable way to see inside a guest that has no working guest
// agent — which is every guest until the tools are installed, and any guest
// that is still in the UEFI shell or mid-install.
//
// The naive approach does not work. Plain `screencapture` grabs whichever Space
// is frontmost, so it returns the developer's editor; `osascript` activation
// does not switch Spaces; and the accessibility API refuses to enumerate UTM's
// windows without a permission a CLI cannot grant itself. Capturing by window
// ID has none of those problems, and does not steal focus.
func Screenshot(vmName, outPath string) error {
	id, err := windowID(vmName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	// -x silences the shutter, -o omits the window shadow.
	out, err := exec.Command("screencapture", "-x", "-o", "-l", strconv.Itoa(id), outPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("capturing window %d: %w: %s", id, err, strings.TrimSpace(string(out)))
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return fmt.Errorf("capture produced no file: %w", err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("capture produced an empty file; the VM window may have closed")
	}
	return nil
}

// windowID finds the CoreGraphics window ID for a VM's display window.
//
// Shells out to `swift`, which ships with the Xcode command line tools. Reading
// CGWindowList from Go directly would mean cgo, and this project is deliberately
// cgo-free — a scripting dependency that is already on any Mac able to build for
// Apple platforms is the cheaper trade.
func windowID(vmName string) (int, error) {
	f, err := os.CreateTemp("", "utmvm-windowid-*.swift")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(f.Name()) }() // scratch

	// Close is checked, and it has to be: an unflushed write leaves a truncated
	// Swift file, and swift then fails with a parse error that reads like the
	// Xcode tools are missing.
	if _, wErr := f.WriteString(windowIDSwift); wErr != nil {
		_ = f.Close()
		return 0, wErr
	}
	if cErr := f.Close(); cErr != nil {
		return 0, cErr
	}

	out, err := exec.Command("swift", f.Name()).Output()
	if err != nil {
		return 0, fmt.Errorf("listing UTM windows (needs Xcode command line tools): %w", err)
	}

	var titles []string
	for _, line := range strings.Split(string(out), "\n") {
		id, title, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		titles = append(titles, title)
		if strings.EqualFold(title, vmName) {
			n, convErr := strconv.Atoi(id)
			if convErr != nil {
				return 0, fmt.Errorf("bad window id %q: %w", id, convErr)
			}
			return n, nil
		}
	}
	// UTM's main window is always present; a missing VM window means the display
	// was never opened, which is what `utmctl start` does. Say so, because the
	// symptom otherwise looks like the VM being absent.
	return 0, fmt.Errorf("no UTM window titled %q (found: %s).\n"+
		"A VM started with `utmctl start` has no display window — use `irgo-winvm vm`, "+
		"which starts it through UTM so a window exists", vmName, strings.Join(titles, ", "))
}

// Runtime screenshots: one per stage, so a run leaves a visual record.
//
// These are NOT the committed ones in docs/screens. Those are evidence chosen
// for documentation; these are every stage of every run, throwaway, and there
// will be hundreds. Mixing them means the repository fills with noise and the
// evidence gets lost in it.

const (
	// shotDirName is where runtime screenshots go, under the tool's own root
	// rather than in the repository.
	shotDirName = "shots"

	// shotSettle is how long to wait before the shot. A stage that has just
	// pressed a key has not finished drawing the result of it.
	shotSettle = 2 * time.Second
)

// ShotDir is where runtime screenshots are written.
func ShotDir() string { return filepath.Join(appRoot(), shotDirName) }

// Shot photographs a VM at a named point in a run and returns the path.
//
// Errors are returned, never fatal to the caller: failing to photograph a boot
// is not a reason to abandon it. The path comes back so the CLI can print it
// while the stage is still on screen, which is the whole point — a stuck boot
// is invisible from the host, and "here is what it looks like right now" is the
// only answer.
func Shot(vmName, stage string) (string, error) {
	if err := os.MkdirAll(ShotDir(), 0o755); err != nil {
		return "", err
	}
	// Ordered by run, then by stage: a directory listing reads as the sequence
	// that happened, which is what somebody looking at a failure wants.
	name := fmt.Sprintf("%s-%s-%s.png", vmName, time.Now().Format("20060102-150405"), stage)
	out := filepath.Join(ShotDir(), name)
	time.Sleep(shotSettle)
	if err := Screenshot(vmName, out); err != nil {
		return "", err
	}
	return out, nil
}
