package conformance

import (
	"fmt"
	"testing"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/ebitengine/purego/objc"
)

// dismissFileDialog waits for an application-modal panel to be on screen and
// aborts its modal session, which is what Cancel does: runModal returns
// something other than OK, and glaze's OpenFile reports "", nil.
//
// Not through glaze's Dispatch, which was the first attempt and cannot work:
// glaze runs the panel inside a block on the main dispatch queue, which is
// serial, so nothing else dispatched there runs until the panel has closed.
// performSelectorOnMainThread goes through the run loop instead, in its common
// modes, and the modal panel's mode is one of them — so abortModal reaches the
// thread runModal is holding. Apple documents abortModal as the call for
// stopping a modal loop from outside it.
//
// modalWindow is read from this goroutine. It is a property read, polled until
// the panel is up; nothing is changed off the main thread.
func dismissFileDialog(_ glaze.WebView, timeout time.Duration) error {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if app.Send(objc.RegisterName("modalWindow")) != 0 {
			app.Send(objc.RegisterName("performSelectorOnMainThread:withObject:waitUntilDone:"),
				objc.RegisterName("abortModal"), objc.ID(0), false)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("no modal panel appeared within %s: OpenFile did not present its dialog", timeout)
}

// checkCaptureExcluded has nothing to read back on macOS, where nocapture is
// unsupported by design and TestNoCapture has already skipped.
func checkCaptureExcluded(*testing.T, glaze.WebView) {}
