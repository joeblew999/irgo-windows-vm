//go:build darwin || windows

package conformance

import (
	"errors"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/glaze/menu"
	"github.com/crgimenes/native/clipboard"
	"github.com/crgimenes/native/mmap"
	"github.com/crgimenes/native/nocapture"
	"github.com/crgimenes/native/openurl"
	"github.com/crgimenes/native/power"
	"github.com/crgimenes/native/singleinstance"
	"github.com/crgimenes/native/tray"
)

// The main thread.
//
// AppKit accepts UI work only from the process's main OS thread, and Win32
// delivers a window's messages only to the thread that created it. The testing
// package runs every Test function on a goroutine of its own, so neither holds
// for a test as written.
//
// glaze's own tests answer this by running every GUI scenario inside TestMain,
// before m.Run, and letting the Test functions assert stored strings. That
// works, but a failure then reads "scenario returned 'no report'", and every
// scenario runs even when -run picked one test.
//
// Instead: init locks the main goroutine to the main thread (init, not
// TestMain — the runtime only guarantees the main goroutine is on the main
// thread up to the point it is locked, and init is the documented place to do
// it), TestMain hands m.Run to another goroutine, and the main goroutine
// then runs whatever a test sends it on mainQueue. A test opens its window
// there, the window's run loop occupies the main thread, and the test itself
// carries on, on its own goroutine, with t.Fatal and subtests working as
// they always do.

func init() { runtime.LockOSThread() }

// mainQueue carries work to the main thread. Unbuffered: a send returns once
// the main goroutine has taken the function.
var mainQueue = make(chan func())

func TestMain(m *testing.M) {
	code := make(chan int, 1)
	go func() { code <- m.Run() }()
	for {
		select {
		case f := <-mainQueue:
			f()
		case c := <-code:
			os.Exit(c)
		}
	}
}

// needGUI skips a test that needs a desktop session when -short asks for the
// headless subset.
//
// -short, because that is the convention glaze's own suite follows, and so
// what anyone running `go test -short` already expects: go:check runs the
// suite that way, and so does app:test, whose binary runs under the guest
// agent in session 0 where there is no window station at all.
func needGUI(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a desktop session and opens a window; -short runs the headless tests only")
	}
}

// uiTimeout bounds every wait on the UI thread. Each step here takes
// milliseconds; ten seconds is a hang, and a hang is recorded as one rather
// than left to -test.timeout, which kills every test after it too.
const uiTimeout = 10 * time.Second

// openWindow creates a glaze window on the main thread, calls load there
// before the run loop starts (Bind and Navigate go in load), and returns once
// the loop is running. Cleanup terminates the loop and destroys the window, so
// no test leaves a window behind whatever it does.
func openWindow(t *testing.T, opts glaze.Options, load func(glaze.WebView)) glaze.WebView {
	t.Helper()
	needGUI(t)

	var w glaze.WebView
	created := make(chan error, 1)
	exited := make(chan struct{})
	mainQueue <- func() {
		defer close(exited)
		var err error
		w, err = glaze.NewWithOptions(opts)
		if err != nil {
			created <- err
			return
		}
		defer w.Destroy()
		w.SetTitle("irgo conformance: " + t.Name())
		w.SetSize(480, 320, glaze.HintNone)
		if load != nil {
			load(w)
		}
		created <- nil
		w.Run()
	}
	if err := <-created; err != nil {
		<-exited
		t.Fatalf("glaze.NewWithOptions: %v", err)
	}
	t.Cleanup(func() {
		w.Terminate()
		select {
		case <-exited:
		case <-time.After(uiTimeout):
			t.Errorf("the window's run loop was still running %s after Terminate; every test after this one will hang", uiTimeout)
		}
	})
	// A dispatched function runs only once the loop is draining, so this is
	// what "the window is up" means. Terminate before then can be lost.
	onUI(t, w, func() {})
	return w
}

// onUI runs f on the UI thread and waits for it.
func onUI(t *testing.T, w glaze.WebView, f func()) {
	t.Helper()
	done := make(chan struct{})
	w.Dispatch(func() {
		defer close(done)
		f()
	})
	select {
	case <-done:
	case <-time.After(uiTimeout):
		t.Fatalf("the UI thread did not run a dispatched function within %s", uiTimeout)
	}
}

// Unsupported, and where that is the documented answer.
//
// STANDING IN FOR AN UPSTREAM FIX — docs/UPSTREAM.md §2. Every package defines
// its own ErrUnsupported instead of wrapping errors.ErrUnsupported, so one
// errors.Is against the standard sentinel matches none of them. The list below
// collapses to that one check once the wrapping is in a released glaze and
// native; errors.ErrUnsupported stays first because it is what this becomes.
var unsupportedErrs = []error{
	errors.ErrUnsupported,
	clipboard.ErrUnsupported,
	mmap.ErrUnsupported,
	power.ErrUnsupported,
	singleinstance.ErrUnsupported,
	openurl.ErrUnsupported,
	nocapture.ErrUnsupported,
	tray.ErrUnsupported,
	menu.ErrUnsupported,
	glaze.ErrIconUnsupported,
}

// documentedUnsupported is where a capability is unsupported by design. Any
// capability not listed here is supposed to work on both darwin and windows,
// and its ErrUnsupported there is a failure: a skip would turn a backend that
// went missing into a quiet row nobody reads.
var documentedUnsupported = map[string][]string{
	// native/nocapture has a Windows backend only (SetWindowDisplayAffinity).
	"nocapture": {"darwin"},
	// Windows takes a process's icon from the executable's resources, decided
	// before the process exists; glaze documents ErrIconUnsupported there.
	"appicon": {"windows"},
}

// require fails the test on err, except that an ErrUnsupported on an OS where
// capability is documented as unsupported skips it, saying so.
func require(t *testing.T, capability string, err error, doing string) {
	t.Helper()
	if err == nil {
		return
	}
	isUnsupported := slices.ContainsFunc(unsupportedErrs, func(s error) bool { return errors.Is(err, s) })
	switch {
	case isUnsupported && slices.Contains(documentedUnsupported[capability], runtime.GOOS):
		t.Skipf("%s is unsupported on %s by design: %v", capability, runtime.GOOS, err)
	case isUnsupported:
		t.Fatalf("%s: %s reported unsupported on %s, where it is supposed to work: %v", doing, capability, runtime.GOOS, err)
	default:
		t.Fatalf("%s: %v", doing, err)
	}
}

// tinyPNG is a 1x1 transparent PNG: enough to prove an icon path accepts and
// decodes an image without shipping an asset.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0d, 0x0a, 0x2d, 0xb4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}
