package conformance

import (
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/crgimenes/glaze"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// macOS captures: screencapture, the system's own tool, by window number or
// by screen rectangle. Out of process, so nothing here draws into, or blocks,
// the window being photographed.
//
// screencapture needs the Screen Recording permission, which belongs to
// whatever started this process (the terminal, or a CI runner's agent), and a
// process without it gets the desktop wallpaper where the window's content
// should be — a picture that looks like a capture and is not. So the
// permission is asked first, with CGPreflightScreenCaptureAccess, which only
// answers and never prompts. No permission is "not captured", with the reason.

var (
	cgOnce    sync.Once
	cgErr     error
	cgAllowed func() bool
)

// screenRecording reports whether this process may capture the screen.
func screenRecording() error {
	cgOnce.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			cgErr = fmt.Errorf("loading CoreGraphics: %w", err)
			return
		}
		purego.RegisterLibFunc(&cgAllowed, lib, "CGPreflightScreenCaptureAccess")
	})
	if cgErr != nil {
		return cgErr
	}
	if !cgAllowed() {
		return errors.New("this process has no Screen Recording permission (CGPreflightScreenCaptureAccess is false); " +
			"grant it to the terminal or runner that started the suite")
	}
	return nil
}

// screencapture runs the tool with args and a temporary output file, and
// decodes what it wrote.
func screencapture(args ...string) (image.Image, error) {
	if err := screenRecording(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "conformance-shot-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "shot.png")
	// -x: no shutter sound. -o (with -l): no window shadow.
	if b, err := exec.Command("screencapture", append(append([]string{"-x"}, args...), out)...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("screencapture %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	img, err := readPNG(out)
	if err != nil {
		return nil, fmt.Errorf("screencapture %s wrote no readable PNG: %w", strings.Join(args, " "), err)
	}
	return img, nil
}

func windowNumber(win objc.ID) int {
	return objc.Send[int](win, objc.RegisterName("windowNumber"))
}

func captureWindowNumber(n int) (image.Image, error) {
	if n <= 0 {
		return nil, fmt.Errorf("the window has no window-server number (%d): it is not on screen", n)
	}
	return screencapture("-o", "-l", strconv.Itoa(n))
}

// grabWindow photographs glaze's NSWindow by its window-server number.
//
// Every read here is from the calling goroutine, not dispatched to the main
// thread: windowNumber is a property read of a window that is on screen, as
// modalWindow is in dismissFileDialog, and a dispatch would queue behind
// whatever block holds the main queue — tray.Run's event loop, or the file
// dialog's modal session.
func grabWindow(w glaze.WebView) (image.Image, error) {
	return captureWindowNumber(windowNumber(objc.ID(uintptr(w.Window()))))
}

// grabDialog photographs the modal panel dismissFileDialog found.
func grabDialog(dialog uintptr) (image.Image, error) {
	return captureWindowNumber(windowNumber(objc.ID(dialog)))
}

// grabMenu has nothing it can honestly photograph on macOS: the menu bar is
// global and drawn by the system, not in any window of this process, and a
// capture of the screen rectangle it occupies returned the desktop wallpaper
// with no menus in it, or black (macOS 27, 30 Sep 2026) — pictures that look
// like captures and are not. So it says so, and the test falls back to its
// window.
func grabMenu(glaze.WebView) (image.Image, error) {
	return nil, errors.New("macOS draws the menu bar outside this process, and a capture of that part of the screen shows the desktop behind it, not the menus")
}

// grabTray photographs this process's status item. AppKit draws it in a
// window of its own (NSStatusBarWindow) and lists that among the
// application's windows, so it is captured like any window, by number.
//
// Read from this goroutine: tray.Run was dispatched to the main queue and is
// running its event loop inside that block, so nothing else dispatched there
// runs until it returns.
func grabTray(glaze.WebView) (image.Image, error) {
	wins := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication")).Send(objc.RegisterName("windows"))
	n := objc.Send[int](wins, objc.RegisterName("count"))
	for i := range n {
		win := wins.Send(objc.RegisterName("objectAtIndex:"), i)
		if strings.Contains(className(win), "StatusBar") {
			img, err := captureWindowNumber(windowNumber(win))
			if err != nil {
				return nil, fmt.Errorf("the status item is drawn outside this process and its window could not be captured: %w", err)
			}
			return img, nil
		}
	}
	return nil, errors.New("AppKit lists no status-bar window for this process: the tray item is not on screen")
}

// className is an object's class name, copied out of the NSString into Go
// memory with -getCString:maxLength:encoding: (4 is NSUTF8StringEncoding).
func className(obj objc.ID) string {
	s := obj.Send(objc.RegisterName("className"))
	if s == 0 {
		return ""
	}
	var buf [256]byte
	if !objc.Send[bool](s, objc.RegisterName("getCString:maxLength:encoding:"), unsafe.Pointer(&buf[0]), len(buf), 4) { // #nosec G103 -- a Go buffer AppKit writes a C string into
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return string(buf[:])
}
