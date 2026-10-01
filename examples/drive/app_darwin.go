package drive

import (
	"errors"
	"image"
	"math"
	"unsafe"

	"github.com/crgimenes/glaze"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

const (
	// NSApplicationActivationPolicyProhibited: no Dock icon, and the app can
	// never become active, so no click or key it receives can bring it
	// forward.
	activationPolicyProhibited = 2
	styleTitled                = 1 << 0
	backingStoreBuffered       = 2
	// Where the window goes, in points from the main screen's top-left. It is
	// ordered behind every other window, so this is only where it would be.
	windowX, windowY = 80, 80
)

// backgroundWindow makes the window glaze is given, so that glaze does not
// make its own: glaze's own window is made key and ordered front, and the
// first one in a process that is not a bundle activates the app. This one is
// created under the Prohibited policy and ordered to the back.
func backgroundWindow(p Page) (unsafe.Pointer, error) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return nil, err
	}
	sel := objc.RegisterName
	app := objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), activationPolicyProhibited)

	// Cocoa's origin is the main screen's bottom-left.
	screen := objc.ID(objc.GetClass("NSScreen")).Send(sel("mainScreen"))
	if screen == 0 {
		return nil, errors.New("no main screen: this needs a desktop session")
	}
	frame := objc.Send[cgRect](screen, sel("frame"))
	top := frame.Size.H - windowY
	win := objc.ID(objc.GetClass("NSWindow")).Send(sel("alloc")).Send(
		sel("initWithContentRect:styleMask:backing:defer:"),
		cgRect{cgPoint{windowX, top - float64(p.Height)}, cgSize{float64(p.Width), float64(p.Height)}},
		uint(styleTitled), uint(backingStoreBuffered), false)
	if win == 0 {
		return nil, errors.New("NSWindow init returned nil")
	}
	win.Send(sel("setTitle:"), objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), p.Title))
	win.Send(sel("setReleasedWhenClosed:"), false)
	win.Send(sel("orderBack:"), objc.ID(0)) // on screen, behind everything, nothing made key
	return handle(uintptr(win)), nil
}

// windowInfo is the window's window-server number (the CGWindowID that
// native/screen captures and native/input routes clicks to) and where its
// content starts inside its frame: below the title bar.
func windowInfo(w glaze.WebView) (uint32, image.Point, error) {
	sel := objc.RegisterName
	win := objc.ID(uintptr(w.Window()))
	n := objc.Send[int](win, sel("windowNumber"))
	if n <= 0 {
		return 0, image.Point{}, errors.New("the window has no window-server number: it is not on screen")
	}
	frame := objc.Send[cgRect](win, sel("frame"))
	content := objc.Send[cgRect](win, sel("contentLayoutRect"))
	// contentLayoutRect is in window coordinates, origin bottom-left.
	top := frame.Size.H - (content.Origin.Y + content.Size.H)
	return uint32(n), image.Pt(int(math.Round(content.Origin.X)), int(math.Round(top))), nil
}

// handle reinterprets a native handle as the unsafe.Pointer glaze takes,
// spelled so go vet's unsafeptr check does not mistake it for Go memory.
func handle(u uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u)) // #nosec G103 -- an NSWindow handle, not Go memory
}
