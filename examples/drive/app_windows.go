package drive

import (
	"errors"
	"fmt"
	"image"
	"syscall"
	"unsafe"

	"github.com/crgimenes/glaze"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procClientToScreen     = user32.NewProc("ClientToScreen")
	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procShowWindow         = user32.NewProc("ShowWindow")
	procAdjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
	procLoadCursorW        = user32.NewProc("LoadCursorW")

	procGetModuleHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procCreateSolidBrush = syscall.NewLazyDLL("gdi32.dll").NewProc("CreateSolidBrush")

	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	// dwmwaExtendedFrameBounds is DWMWA_EXTENDED_FRAME_BOUNDS.
	dwmwaExtendedFrameBounds = 9

	wsCaption        = 0x00C00000
	wsSysMenu        = 0x00080000
	wsMinimizeBox    = 0x00020000
	wsExNoActivate   = 0x08000000
	swShowNoActivate = 4
	idcArrow         = 32512
	white            = 0xffffff

	// Where the window goes, in pixels from the main screen's top-left.
	windowX, windowY = 80, 80
)

type winRect struct{ Left, Top, Right, Bottom int32 }
type winPoint struct{ X, Y int32 }

// wndClassExW is WNDCLASSEXW.
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// backgroundWindow makes the window glaze is given, so that glaze does not
// make its own. glaze's window is an ordinary one, and both glaze's start-up
// (ShowWindow SW_SHOW, then MoveFocus, which makes WebView2 call SetFocus)
// and a background click (Chromium focuses its window on mouse-down, which
// activates the top-level window) can make it the foreground window: the
// activation caveat in native's input README. This window has
// WS_EX_NOACTIVATE, the counterpart of the macOS window's Prohibited policy,
// and is shown with SW_SHOWNOACTIVATE before glaze sees it, as native's
// testwin does. It is on top of the Z order, not behind everything as on
// macOS, because Chromium drops a wheel event whose point is over another
// process's window.
//
// The class's window procedure is DefWindowProcW itself (glaze subclasses the
// window for what it needs), so no Go callback is handed to Windows.
func backgroundWindow(p Page) (unsafe.Pointer, error) {
	cls, err := syscall.UTF16PtrFromString("irgo-drive")
	if err != nil {
		return nil, err
	}
	title, err := syscall.UTF16PtrFromString(p.Title)
	if err != nil {
		return nil, err
	}
	instance, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	brush, _, _ := procCreateSolidBrush.Call(white) // what shows before WebView2 draws
	wc := wndClassExW{
		lpfnWndProc:   procDefWindowProcW.Addr(),
		hInstance:     instance,
		hCursor:       cursor,
		hbrBackground: brush,
		lpszClassName: cls,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 { // #nosec G103 -- a Win32 in-parameter
		return nil, fmt.Errorf("RegisterClassExW: %w", err)
	}
	const style = wsCaption | wsSysMenu | wsMinimizeBox
	r := winRect{0, 0, int32(p.Width), int32(p.Height)}                                                                    // #nosec G115 -- a window size
	_, _, _ = procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), style, 0, wsExNoActivate)                           // #nosec G103 -- a Win32 in/out-parameter
	hwnd, _, err := procCreateWindowExW.Call(wsExNoActivate, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), // #nosec G103 -- Win32 strings
		style, windowX, windowY, uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0, 0, instance, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowExW: %w", err)
	}
	_, _, _ = procShowWindow.Call(hwnd, swShowNoActivate)
	return handle(hwnd), nil
}

// onReady moves keyboard focus into the page once it has loaded, before the
// test is told it is ready. glaze does it once at start-up, but on a cold
// WebView2 start that can run before Chromium's windows exist, and a window
// that is never activated never gets the WM_SETFOCUS that would retry it
// (measured by native's testwin on the windows-11-arm runner). It makes the
// window active inside its own thread only; WS_EX_NOACTIVATE keeps the
// foreground where it is.
func onReady(w glaze.WebView) { w.Focus() }

// handle reinterprets a native handle as the unsafe.Pointer glaze takes,
// spelled so go vet's unsafeptr check does not mistake it for Go memory.
func handle(u uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u)) // #nosec G103 -- an HWND, not Go memory
}

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
