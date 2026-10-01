package drive

import (
	"errors"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")

	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION, which
// is enough for the image name of any process in the session.
const processQueryLimitedInformation = 0x1000

// Frontmost is the foreground window, its title, and the process that owns
// it, by executable name. A test compares it before and after driving an
// app; see the macOS Frontmost. The names are what make a change readable: an
// HWND and a pid alone said nothing about what took the foreground on a CI
// runner (1 Oct 2026).
func Frontmost() (App, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return App{}, errors.New("no foreground window (a desktop being switched, or no interactive session)")
	}
	var pid uint32
	_, _, _ = procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid))) // #nosec G103 -- a Win32 out-parameter
	title := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title))) // #nosec G103 -- a Win32 out-parameter
	return App{
		Name:   processName(pid),
		PID:    int(pid),
		Window: uint32(hwnd), // #nosec G115 -- HWNDs are 32-bit values on every Windows
		Title:  syscall.UTF16ToString(title[:n]),
	}, nil
}

// processName is pid's executable, without its directory, or "?" when the
// process cannot be opened (one at a higher integrity level, or gone).
func processName(pid uint32) string {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return "?"
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf))
	if ok, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); ok == 0 { // #nosec G103 -- Win32 out-parameters
		return "?"
	}
	return filepath.Base(syscall.UTF16ToString(buf[:size]))
}

// stepForeground is Frontmost, for the session's trace: cheap on Windows,
// so every step records it.
func stepForeground() (App, bool, error) {
	a, err := Frontmost()
	return a, true, err
}
