package drive

import (
	"errors"
	"image"
	"syscall"
	"unsafe"

	"github.com/crgimenes/glaze"
)

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	procGetWindowRect  = user32.NewProc("GetWindowRect")
	procClientToScreen = user32.NewProc("ClientToScreen")

	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

// dwmwaExtendedFrameBounds is DWMWA_EXTENDED_FRAME_BOUNDS.
const dwmwaExtendedFrameBounds = 9

type winRect struct{ Left, Top, Right, Bottom int32 }
type winPoint struct{ X, Y int32 }

// backgroundWindow lets glaze make the window on Windows. Whether glaze's
// window takes the foreground there has not been measured from this package:
// the drive tests run on CI runners and the VM, never on a desktop someone is
// using.
func backgroundWindow(Page) (unsafe.Pointer, error) { return nil, nil }

// windowInfo is the HWND, as the ID native/screen and native/input take, and
// where the client area starts inside the window's frame, in pixels.
//
// "The frame" is the one native/input's Windows backend counts window-relative
// coordinates from: DWM's extended frame bounds, the window as you see it.
// GetWindowRect includes the invisible resize borders (7 px on Windows 11),
// and using it put every ClickAt 7 px right (measured on the windows-11-arm
// runner, 1 Oct 2026). GetWindowRect stays as the fallback where DWM does not
// answer, as in the backend.
func windowInfo(w glaze.WebView) (uint32, image.Point, error) {
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		return 0, image.Point{}, errors.New("glaze returned no window handle")
	}
	var r winRect
	if hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); hr != 0 { // #nosec G103 -- a Win32 out-parameter
		if ok, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 { // #nosec G103 -- a Win32 out-parameter
			return 0, image.Point{}, err
		}
	}
	var p winPoint
	if ok, _, err := procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&p))); ok == 0 { // #nosec G103 -- a Win32 out-parameter
		return 0, image.Point{}, err
	}
	return uint32(hwnd), image.Pt(int(p.X-r.Left), int(p.Y-r.Top)), nil
}
