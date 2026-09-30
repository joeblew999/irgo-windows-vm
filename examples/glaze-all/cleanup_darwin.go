package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crgimenes/glaze"
	"github.com/ebitengine/purego/objc"
)

// Finder, through osascript. The same permission the owner's terminal already
// has to script Finder; if it is refused, osascript says so (-1743) and the
// run records that as the failure, rather than opening a window it cannot
// close.

// finderWindows lists every Finder window as "id<TAB>POSIX path".
//
// The ids come first, from one `id of every Finder window`, and the paths are
// looked up afterwards, each in its own try. Reading both in one loop with the
// failures skipped dropped windows from the list at random while Finder was
// busy — measured 30 Sep 2026, three of six missing in one listing — and a
// window missing from the "before" list is a window this run would have
// believed it opened, and closed. An id is always listed; a path that could
// not be read is left empty, and an empty place matches nothing.
//
// The target is coerced outside the POSIX path call: inside the Finder tell
// block `POSIX path of` fails with "Unknown object type" (-1731). A window with
// no folder behind it (Recents, AirDrop) has no alias and gets no path.
const finderWindows = `tell application "Finder" to set ids to id of every Finder window
set out to ""
repeat with i in ids
	set p to ""
	try
		tell application "Finder" to set a to (target of Finder window id i) as alias
		set p to POSIX path of a
	end try
	set out to out & i & tab & p & linefeed
end repeat
return out`

func listFileManagerWindows() ([]fmWindow, error) {
	out, err := exec.Command("osascript", "-e", finderWindows).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("osascript (listing Finder windows): %w: %s", err, strings.TrimSpace(string(out)))
	}
	var ws []fmWindow
	for _, line := range strings.Split(string(out), "\n") {
		id, place, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("osascript gave a Finder window id %q: %w", id, err)
		}
		ws = append(ws, fmWindow{ID: n, Place: place})
	}
	return ws, nil
}

func closeFileManagerWindow(id uint64) error {
	out, err := exec.Command("osascript", "-e",
		fmt.Sprintf(`tell application "Finder" to close Finder window id %d`, id)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func placeMatches(place, dir string) bool { return samePath(place, dir, filepath.EvalSymlinks) }

// finderFront is the front Finder window's id, then what is selected in it,
// one POSIX path per line.
const finderFront = `tell application "Finder" to set f to id of front Finder window
tell application "Finder" to set s to selection
set out to (f as string) & linefeed
repeat with x in s
	try
		tell application "Finder" to set a to x as alias
		set out to out & (POSIX path of a) & linefeed
	end try
end repeat
return out`

// shownInExisting reports whether Finder answered by selecting target in a
// window that was already open, rather than opening one. It does that for
// Reveal when a window onto the parent folder exists — measured 30 Sep 2026
// with five such windows left by earlier runs: no new window, the existing one
// brought forward with the directory selected. That window was not opened by
// this run, so it is not this run's to close.
func shownInExisting(before []fmWindow, target string) (bool, error) {
	out, err := exec.Command("osascript", "-e", finderFront).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("osascript (reading Finder's front window): %w: %s", err, strings.TrimSpace(string(out)))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	front, err := strconv.ParseUint(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil {
		return false, fmt.Errorf("osascript gave a front window id %q: %w", lines[0], err)
	}
	return existingShows(before, front, lines[1:], target, placeMatches), nil
}

// dismissFileDialog ends the NSOpenPanel's runModal with NSModalResponseAbort,
// which glaze reports as a cancel: OpenFile returns "", nil.
//
// abortModal is the call Apple documents for stopping a modal loop from
// outside it — stopModal is for a callout from the modal window itself. It is
// sent through Dispatch, so it runs on the main thread: the modal loop runs
// the main queue (NSModalPanelRunLoopMode is one of the common modes), which is
// also what lets the probe's other Dispatch calls work while a panel is up.
func dismissFileDialog(_ glaze.WebView, _ string) error {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	if app.Send(objc.RegisterName("modalWindow")) == 0 {
		return errors.New("no modal window is up to dismiss")
	}
	app.Send(objc.RegisterName("abortModal"))
	return nil
}
