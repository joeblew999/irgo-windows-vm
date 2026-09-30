package utmvm

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// utmWindow is one of UTM's on-screen windows.
type utmWindow struct {
	id    int
	title string
}

// utmWindows lists UTM's windows with CGWindowListCopyWindowInfo, called
// through purego so the tool stays cgo-free and needs no Xcode tools (it used
// to compile a Swift helper on every call).
//
// .optionOnScreenOnly is deliberately not passed: the VM window is usually on
// another Space and would not be listed. Titles of other apps' windows need
// Screen Recording permission, which screencapture needs anyway.
func utmWindows() ([]utmWindow, error) {
	if err := loadCG(); err != nil {
		return nil, err
	}
	const excludeDesktopElements = 1 << 4
	list := cg.copyWindowInfo(excludeDesktopElements, 0)
	if list == 0 {
		return nil, fmt.Errorf("CGWindowListCopyWindowInfo returned nothing")
	}
	defer cg.release(list)

	var out []utmWindow
	for i := range cg.count(list) {
		d := cg.at(list, i)
		if cg.str(cg.getValue(d, cg.kOwner)) != "UTM" {
			continue
		}
		title := cg.str(cg.getValue(d, cg.kName))
		if title == "" {
			continue
		}
		var id int32
		const cfNumberSInt32Type = 3
		if !cg.getNum(cg.getValue(d, cg.kNumber), cfNumberSInt32Type, unsafe.Pointer(&id)) {
			continue
		}
		out = append(out, utmWindow{id: int(id), title: title})
	}
	return out, nil
}

// cgFuncs holds the CoreGraphics and CoreFoundation calls utmWindows needs.
type cgFuncs struct {
	copyWindowInfo func(option, relativeTo uint32) uintptr
	count          func(array uintptr) int
	at             func(array uintptr, i int) uintptr
	getValue       func(dict, key uintptr) uintptr
	mkString       func(alloc uintptr, s string, encoding uint32) uintptr
	getCString     func(s uintptr, buf *byte, n int, encoding uint32) bool
	getNum         func(n uintptr, typ int, out unsafe.Pointer) bool
	release        func(ref uintptr)

	kOwner, kName, kNumber uintptr
}

var (
	cgOnce sync.Once
	cgErr  error
	cg     cgFuncs
)

const cfStringEncodingUTF8 = 0x08000100

func loadCG() error {
	cgOnce.Do(func() {
		core, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_GLOBAL)
		if err != nil {
			cgErr = fmt.Errorf("loading CoreGraphics: %w", err)
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_GLOBAL)
		if err != nil {
			cgErr = fmt.Errorf("loading CoreFoundation: %w", err)
			return
		}
		purego.RegisterLibFunc(&cg.copyWindowInfo, core, "CGWindowListCopyWindowInfo")
		purego.RegisterLibFunc(&cg.count, cf, "CFArrayGetCount")
		purego.RegisterLibFunc(&cg.at, cf, "CFArrayGetValueAtIndex")
		purego.RegisterLibFunc(&cg.getValue, cf, "CFDictionaryGetValue")
		purego.RegisterLibFunc(&cg.mkString, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cg.getCString, cf, "CFStringGetCString")
		purego.RegisterLibFunc(&cg.getNum, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&cg.release, cf, "CFRelease")
		// Kept for the life of the process; three small strings.
		cg.kOwner = cg.mkString(0, "kCGWindowOwnerName", cfStringEncodingUTF8)
		cg.kName = cg.mkString(0, "kCGWindowName", cfStringEncodingUTF8)
		cg.kNumber = cg.mkString(0, "kCGWindowNumber", cfStringEncodingUTF8)
	})
	return cgErr
}

// str copies a CFString (0 for none) into a Go string.
func (c *cgFuncs) str(ref uintptr) string {
	if ref == 0 {
		return ""
	}
	buf := make([]byte, 1024)
	if !c.getCString(ref, &buf[0], len(buf), cfStringEncodingUTF8) {
		return ""
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}
