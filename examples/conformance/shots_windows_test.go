package conformance

import (
	"errors"
	"fmt"
	"image"
	"syscall"
	"unsafe"

	"github.com/crgimenes/glaze"
)

// Windows captures, through GDI and DWM from user32, gdi32 and dwmapi. No
// cgo, like the rest.
//
// A window is photographed with PrintWindow and PW_RENDERFULLCONTENT, which
// asks DWM for what it composited rather than asking the window to paint
// itself into a DC: WebView2 draws with DirectComposition in another process,
// and a plain WM_PRINT or a BitBlt from the window's DC gets black where the
// page should be. The notification area belongs to Explorer, not to this
// process, so it is copied from the screen instead.

var (
	gdi32                     = syscall.NewLazyDLL("gdi32.dll")
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procPrintWindow           = user32.NewProc("PrintWindow")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procFindWindowExW         = user32.NewProc("FindWindowExW")
	procCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp   = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procBitBlt                = gdi32.NewProc("BitBlt")
	procGetDIBits             = gdi32.NewProc("GetDIBits")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procDeleteDC              = gdi32.NewProc("DeleteDC")
	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	pwRenderFullContent      = 0x2        // PW_RENDERFULLCONTENT
	srcCopy                  = 0x00CC0020 // SRCCOPY
	captureBlt               = 0x40000000 // CAPTUREBLT: include layered windows
	dibRGBColors             = 0          // DIB_RGB_COLORS
	dwmwaExtendedFrameBounds = 9          // DWMWA_EXTENDED_FRAME_BOUNDS
	maxInvisibleBorderPixels = 32
	bitmapInfoHeaderSize     = 40
	bitsPerPixel             = 32
	biRGB                    = 0
)

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) w() int { return int(r.Right - r.Left) }
func (r rect) h() int { return int(r.Bottom - r.Top) }

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// grabWindow photographs glaze's window.
func grabWindow(w glaze.WebView) (image.Image, error) {
	return printWindow(uintptr(w.Window()))
}

// grabMenu is the window: on Windows the menu bar belongs to the HWND.
func grabMenu(w glaze.WebView) (image.Image, error) { return grabWindow(w) }

// grabDialog photographs the file dialog dismissFileDialog found.
func grabDialog(dialog uintptr) (image.Image, error) { return printWindow(dialog) }

// grabTray copies the notification area off the screen: Explorer's
// TrayNotifyWnd inside Shell_TrayWnd, or the right-hand end of the taskbar
// where Windows 11 has no such child. Windows 11 puts a new icon in the
// overflow until the user pins it, so the icon itself may not be in the
// picture; the area it was added to is.
func grabTray(glaze.WebView) (image.Image, error) {
	tray := findWindow("Shell_TrayWnd", 0)
	if tray == 0 {
		return nil, errors.New("there is no taskbar (Shell_TrayWnd) on this desktop")
	}
	var r rect
	if notify := findWindow("TrayNotifyWnd", tray); notify != 0 && windowRect(notify, &r) == nil && r.w() > 0 {
		return screenRect(r)
	}
	if err := windowRect(tray, &r); err != nil {
		return nil, err
	}
	if r.w() > 480 {
		r.Left = r.Right - 480
	}
	return screenRect(r)
}

func findWindow(class string, parent uintptr) uintptr {
	name, err := syscall.UTF16PtrFromString(class)
	if err != nil {
		return 0
	}
	var h uintptr
	if parent == 0 {
		h, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(name)), 0)
	} else {
		h, _, _ = procFindWindowExW.Call(parent, 0, uintptr(unsafe.Pointer(name)), 0)
	}
	return h
}

func windowRect(hwnd uintptr, r *rect) error {
	if ok, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(r))); ok == 0 {
		return fmt.Errorf("GetWindowRect: %v", err)
	}
	return nil
}

// printWindow renders hwnd through DWM into a bitmap and reads it back,
// cropped to the frame DWM draws: GetWindowRect includes the invisible resize
// borders Windows 10 and 11 put around a window, which PrintWindow fills with
// nothing.
func printWindow(hwnd uintptr) (image.Image, error) {
	var r rect
	if err := windowRect(hwnd, &r); err != nil {
		return nil, err
	}
	if r.w() <= 0 || r.h() <= 0 {
		return nil, fmt.Errorf("the window is %dx%d: it is not on screen", r.w(), r.h())
	}
	img, err := withBitmap(r.w(), r.h(), func(mem uintptr) error {
		if ok, _, err := procPrintWindow.Call(hwnd, mem, pwRenderFullContent); ok == 0 {
			return fmt.Errorf("PrintWindow: %v", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var ext rect
	if hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds, uintptr(unsafe.Pointer(&ext)), unsafe.Sizeof(ext)); hr == 0 {
		// Only a crop that looks like borders: a process that is not DPI
		// aware gets the two rectangles in different units.
		l, t, rr, b := ext.Left-r.Left, ext.Top-r.Top, r.Right-ext.Right, r.Bottom-ext.Bottom
		if l >= 0 && t >= 0 && rr >= 0 && b >= 0 && max(l, t, rr, b) <= maxInvisibleBorderPixels {
			img = img.SubImage(image.Rect(int(l), int(t), r.w()-int(rr), r.h()-int(b))).(*image.RGBA)
		}
	}
	return img, nil
}

// screenRect copies a rectangle of the screen.
func screenRect(r rect) (image.Image, error) {
	return withBitmap(r.w(), r.h(), func(mem uintptr) error {
		screen, _, _ := procGetDC.Call(0)
		if screen == 0 {
			return errors.New("GetDC(screen) failed")
		}
		defer func() { _, _, _ = procReleaseDC.Call(0, screen) }()
		if ok, _, err := procBitBlt.Call(mem, 0, 0, uintptr(r.w()), uintptr(r.h()), screen, uintptr(r.Left), uintptr(r.Top), srcCopy|captureBlt); ok == 0 {
			return fmt.Errorf("BitBlt from the screen: %v", err)
		}
		return nil
	})
}

// withBitmap gives draw a memory DC holding a w x h bitmap compatible with the
// screen, and returns what was drawn into it as an image.
func withBitmap(w, h int, draw func(mem uintptr) error) (*image.RGBA, error) {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("GetDC(screen) failed: no desktop to capture from")
	}
	defer func() { _, _, _ = procReleaseDC.Call(0, screen) }()
	mem, _, _ := procCreateCompatibleDC.Call(screen)
	if mem == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer func() { _, _, _ = procDeleteDC.Call(mem) }()
	bmp, _, _ := procCreateCompatibleBmp.Call(screen, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap(%dx%d) failed", w, h)
	}
	defer func() { _, _, _ = procDeleteObject.Call(bmp) }()
	old, _, _ := procSelectObject.Call(mem, bmp)
	err := draw(mem)
	_, _, _ = procSelectObject.Call(mem, old) // GetDIBits wants the bitmap not selected
	if err != nil {
		return nil, err
	}

	hdr := bitmapInfoHeader{
		Size: bitmapInfoHeaderSize, Width: int32(w), Height: -int32(h), // negative: top-down rows
		Planes: 1, BitCount: bitsPerPixel, Compression: biRGB,
	}
	// BITMAPINFO is the header followed by a colour table, which BI_RGB at 32
	// bits per pixel does not have; the spare room is a guard, not a table.
	var bmi struct {
		hdr   bitmapInfoHeader
		spare [4]uint32
	}
	bmi.hdr = hdr
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if n, _, err := procGetDIBits.Call(mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&bmi)), dibRGBColors); int(n) != h {
		return nil, fmt.Errorf("GetDIBits read %d of %d rows: %v", n, h, err)
	}
	// GDI hands back BGRX; the X byte is not alpha.
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 0xff
	}
	return img, nil
}
