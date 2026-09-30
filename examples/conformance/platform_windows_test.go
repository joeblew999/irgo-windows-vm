package conformance

import (
	"fmt"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/crgimenes/glaze"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procGetWindow                = user32.NewProc("GetWindow")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowDisplayAffinity = user32.NewProc("GetWindowDisplayAffinity")
)

const (
	gwEnabledPopup = 6      // GW_ENABLEDPOPUP
	wmClose        = 0x0010 // WM_CLOSE
	wdaNone        = 0      // WDA_NONE
)

// dismissFileDialog waits for the dialog glaze owns to the window to appear,
// and closes it the way the title bar's X does, which the Common Item Dialog
// treats as Cancel: Show returns ERROR_CANCELLED and OpenFile reports "", nil.
//
// Found through the OS, not through glaze: GetWindow(GW_ENABLEDPOPUP) is the
// enabled popup the window owns, and answers the window itself while there is
// none. So "the dialog is on screen" is Windows' answer, not the library's.
func dismissFileDialog(w glaze.WebView, timeout time.Duration) error {
	owner := uintptr(w.Window())
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dlg, _, _ := procGetWindow.Call(owner, gwEnabledPopup)
		if dlg != 0 && dlg != owner {
			var buf [64]uint16
			n, _, _ := procGetClassNameW.Call(dlg, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			if r, _, err := procPostMessageW.Call(dlg, wmClose, 0, 0); r == 0 {
				return fmt.Errorf("the dialog (window class %q) is up, and WM_CLOSE could not be posted to it: %v", syscall.UTF16ToString(buf[:n]), err)
			}
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the window owned no popup within %s: OpenFile did not present its dialog", timeout)
}

// checkCaptureExcluded reads the window's display affinity back from Windows.
// native sets WDA_MONITOR, deliberately rather than WDA_EXCLUDEFROMCAPTURE;
// either keeps the window out of a capture, and WDA_NONE is the failure.
func checkCaptureExcluded(t *testing.T, w glaze.WebView) {
	t.Helper()
	var aff uint32
	var ok uintptr
	var callErr error
	onUI(t, w, func() {
		ok, _, callErr = procGetWindowDisplayAffinity.Call(uintptr(w.Window()), uintptr(unsafe.Pointer(&aff)))
	})
	if ok == 0 {
		t.Fatalf("GetWindowDisplayAffinity: %v", callErr)
	}
	if aff == wdaNone {
		t.Fatalf("Protect returned nil and the window's display affinity is still WDA_NONE: it can be captured")
	}
}
