package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/crgimenes/glaze"
)

// Explorer, through user32 — EnumWindows and WM_CLOSE, which is what the close
// button sends. Not Shell.Application's Windows(): measured on the VM on 30 Sep
// 2026, it listed one of the two Explorer windows on screen, missing the one
// whose navigation had failed. Every folder window is a top-level
// CabinetWClass, whatever state it is in.
//
// explorer.exe itself is never touched. Killing it takes the taskbar with it,
// and Windows did not restart the shell when that was tried.

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	procEnumWindows        = user32.NewProc("EnumWindows")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetClassNameW      = user32.NewProc("GetClassNameW")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW       = user32.NewProc("PostMessageW")
	enumMu                 sync.Mutex
	enumFound              []topWindow
	enumCallback           = syscall.NewCallback(enumProc) // once: callbacks are never freed
)

const wmClose = 0x0010

type topWindow struct {
	hwnd         uintptr
	pid          uint32
	class, title string
}

func enumProc(hwnd, _ uintptr) uintptr {
	if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
		return 1
	}
	var pid uint32
	_, _, _ = procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	enumFound = append(enumFound, topWindow{hwnd: hwnd, pid: pid, class: windowString(procGetClassNameW, hwnd), title: windowString(procGetWindowTextW, hwnd)})
	return 1
}

func windowString(p *syscall.LazyProc, hwnd uintptr) string {
	buf := make([]uint16, 512)
	n, _, _ := p.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

// topWindows lists the visible top-level windows on this desktop.
func topWindows() ([]topWindow, error) {
	if err := procEnumWindows.Find(); err != nil {
		return nil, err
	}
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = nil
	if r, _, err := procEnumWindows.Call(enumCallback, 0); r == 0 {
		return nil, fmt.Errorf("EnumWindows: %w", err)
	}
	return append([]topWindow(nil), enumFound...), nil
}

func listFileManagerWindows() ([]fmWindow, error) {
	all, err := topWindows()
	if err != nil {
		return nil, err
	}
	var ws []fmWindow
	for _, w := range all {
		if w.class == "CabinetWClass" {
			ws = append(ws, fmWindow{ID: uint64(w.hwnd), Place: w.title})
		}
	}
	return ws, nil
}

func closeFileManagerWindow(id uint64) error { return postClose(uintptr(id)) }

func postClose(hwnd uintptr) error {
	if r, _, err := procPostMessageW.Call(hwnd, wmClose, 0, 0); r == 0 {
		return fmt.Errorf("PostMessage(WM_CLOSE): %w", err)
	}
	return nil
}

func placeMatches(place, dir string) bool { return titleShows(place, dir) }

// shownInExisting always says no: Explorer opens a new window for every call,
// which is what left six of them on the VM. If that ever changes, the probe
// reports "no window appeared" rather than guessing.
func shownInExisting([]fmWindow, string) (bool, error) { return false, nil }

// dismissFileDialog closes the Common Item Dialog the way its close button
// does. Show then returns HRESULT_FROM_WIN32(ERROR_CANCELLED), which glaze
// reports as a cancel: OpenFile returns "", nil. The dialog is found as a
// top-level #32770 of this process carrying the title the probe gave it.
func dismissFileDialog(_ glaze.WebView, title string) error {
	me := uint32(os.Getpid())
	deadline := time.Now().Add(3 * time.Second)
	for {
		all, err := topWindows()
		if err != nil {
			return err
		}
		for _, w := range all {
			if w.pid == me && w.class == "#32770" && w.title == title {
				return postClose(w.hwnd)
			}
		}
		if time.Now().After(deadline) {
			return errors.New("no dialog window titled " + fmt.Sprintf("%q", title) + " belongs to this process")
		}
		time.Sleep(pollEvery)
	}
}
