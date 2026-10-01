//go:build darwin || windows

package conformance

import (
	"flag"
	"image"
	"os"
	"runtime"
	"testing"

	"github.com/crgimenes/glaze"
	"github.com/joeblew999/irgo-windows-vm/examples/shots"
)

// Screenshots.
//
// Every test that opens a window photographs it at the moment that shows what
// the test proved — the page loaded, the tray up, the menu installed, the
// dialog open — when -conformance.shots (or $CONFORMANCE_SHOTS) names a
// directory. The picture is written there as <os>/<TestName>.png, os being
// "mac" or "windows", the names internal/glazecheck files a target's record
// under.
//
// How a picture is judged, written and reported — a capture never fails a
// test, a one-colour frame is not published, the two lines glazecheck reads —
// is examples/shots, which the VM suite shares.
//
// The mechanism is per OS and cgo-free like the rest: PrintWindow with
// PW_RENDERFULLCONTENT on Windows (shots_windows_test.go), screencapture -l
// with the NSWindow's windowNumber on macOS (shots_darwin_test.go).

var shotsDir = flag.String("conformance.shots", os.Getenv("CONFORMANCE_SHOTS"),
	"directory to write each windowed test's screenshot into, as <os>/<TestName>.png; empty takes none")

// osDir is the directory a target's pictures go in: glazecheck's target names.
func osDir() string {
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return runtime.GOOS
}

// grab takes one picture.
type grab func() (image.Image, error)

// shoot captures one picture for t with g and records what happened.
func shoot(t *testing.T, g grab) {
	t.Helper()
	record(t, attempt(g, ""))
}

// shootExpecting is shoot where a black frame is the expected result, and
// blackMeans says why.
func shootExpecting(t *testing.T, blackMeans string, g grab) {
	t.Helper()
	record(t, attempt(g, blackMeans))
}

// shootOr tries g, and when it cannot produce a picture takes fallback
// instead, saying so beside the picture: fallbackIs names what the fallback
// shows, and the note carries why g did not work. For the tray and the menu
// bar, which on macOS are drawn outside the process and not always
// capturable: the window the test opened is still a picture of the test.
func shootOr(t *testing.T, g grab, fallback grab, fallbackIs string) {
	t.Helper()
	first := attempt(g, "")
	if first.Err == nil || *shotsDir == "" {
		record(t, first)
		return
	}
	second := attempt(fallback, "")
	if second.Err == nil {
		second.Note = fallbackIs + "; " + first.Err.Error()
	}
	record(t, second)
}

// attempt takes a picture with g, or nothing when no directory was given.
func attempt(g grab, blackMeans string) shots.Taken {
	if *shotsDir == "" {
		return shots.Taken{}
	}
	return shots.Take(g, blackMeans)
}

// record writes the picture and logs the line glazecheck reads.
func record(t *testing.T, r shots.Taken) {
	t.Helper()
	if *shotsDir == "" {
		return
	}
	shots.Record(t, *shotsDir, osDir()+"/"+shots.Name(t), r)
}

// labelled is a load function that puts the test's name in its window, so the
// picture of a window that has no page of its own says which test it is.
func labelled(t *testing.T) func(glaze.WebView) {
	return func(w glaze.WebView) {
		w.SetHtml(`<!doctype html><html><body style="font:16px -apple-system,Segoe UI,sans-serif;margin:24px">` +
			`<h2 style="margin:0 0 8px">` + t.Name() + `</h2><p>irgo conformance suite</p></body></html>`)
	}
}
