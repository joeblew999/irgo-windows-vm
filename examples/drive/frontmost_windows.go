package drive

import (
	"errors"
	"fmt"
	"unsafe"
)

var (
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

// Frontmost names the foreground window and the process that owns it. A test
// compares it before and after driving an app; see the macOS Frontmost.
func Frontmost() (string, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", errors.New("no foreground window (a desktop being switched, or no interactive session)")
	}
	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid))) // #nosec G103 -- a Win32 out-parameter
	return fmt.Sprintf("window %#x of process %d", hwnd, pid), nil
}
