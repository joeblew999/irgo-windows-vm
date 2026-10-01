package vmconformance

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"image"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/crgimenes/glaze"
	"github.com/joeblew999/irgo-windows-vm/examples/shots"
)

var (
	shotsDir = flag.String("vmconformance.shots", "",
		"directory to write each TestSession test's picture into, as vm/<Test>.png; empty takes none")
	devUser = flag.String("vmconformance.user", "dev", "the account the VM logs itself in as")
)

// The main thread, for the one test that opens a glaze window. As in
// examples/conformance: init locks the main goroutine to its thread, TestMain
// runs the tests elsewhere and serves mainQueue, and the window's run loop
// gets the thread it was created on.

func init() { runtime.LockOSThread() }

var mainQueue = make(chan func())

func TestMain(m *testing.M) {
	code := make(chan int, 1)
	go func() { code <- m.Run() }()
	for {
		select {
		case f := <-mainQueue:
			f()
		case c := <-code:
			os.Exit(c)
		}
	}
}

// Where a test runs. Each test says where it belongs and skips anywhere
// else, so a binary run by hand in the wrong place reports skips, not
// failures that describe the wrong account.

// asSystem skips t unless this process is LocalSystem.
func asSystem(t *testing.T) {
	t.Helper()
	if u, err := user.Current(); err != nil || u.Uid != "S-1-5-18" {
		name := "an unknown account"
		if err == nil {
			name = u.Username
		}
		t.Skipf("reads what only an administrator's token can; vm-check runs it as SYSTEM through the guest agent, and this is %s", name)
	}
}

// inSession skips t unless this process is in an interactive session.
func inSession(t *testing.T) {
	t.Helper()
	if sessionID() == 0 {
		t.Skip("needs dev's desktop session; vm-check runs the TestSession tests there, through app-create -gui")
	}
}

// evidence records what a check read, kept beside its result whatever the
// outcome.
func evidence(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf("evidence: "+format, args...)
}

// fact records something about the VM at the top of its section.
func fact(t *testing.T, key, value string) {
	t.Helper()
	t.Logf("fact: %s=%s", key, value)
}

// shoot photographs what t checked, when a directory was given.
func shoot(t *testing.T, grab func() (image.Image, error)) {
	t.Helper()
	if *shotsDir == "" {
		return
	}
	shots.Record(t, *shotsDir, "vm/"+shots.Name(t), shots.Take(grab, ""))
}

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procProcessIdToSessionId = kernel32.NewProc("ProcessIdToSessionId")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func sessionID() uint32 {
	var id uint32
	if r, _, _ := procProcessIdToSessionId.Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&id))); r == 0 {
		return 0
	}
	return id
}

// bootTime is when Windows last started: now less its uptime.
func bootTime() time.Time {
	ms, _, _ := procGetTickCount64.Call()
	return time.Now().Add(-time.Duration(ms) * time.Millisecond)
}

// freeBytes is the space free to this process on the volume holding path.
func freeBytes(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if r, _, err := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0); r == 0 {
		return 0, err
	}
	return free, nil
}

// The registry, read through the syscall package: exact values, no parsing of
// reg.exe's output, and no process per read.

var errNoValue = errors.New("no such registry value")

// regRead returns a value's type and raw bytes; errNoValue when the key or
// the value is not there.
func regRead(root syscall.Handle, path, name string) (uint32, []byte, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, nil, err
	}
	var k syscall.Handle
	if err := syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ, &k); err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return 0, nil, errNoValue
		}
		return 0, nil, err
	}
	defer func() { _ = syscall.RegCloseKey(k) }()
	n16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, nil, err
	}
	var typ, n uint32
	if err := syscall.RegQueryValueEx(k, n16, nil, &typ, nil, &n); err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return 0, nil, errNoValue
		}
		return 0, nil, err
	}
	buf := make([]byte, n)
	if n > 0 {
		if err := syscall.RegQueryValueEx(k, n16, nil, &typ, &buf[0], &n); err != nil {
			return 0, nil, err
		}
	}
	return typ, buf[:n], nil
}

// hklm and hkcu name the two roots the checks read, for messages.
func rootName(root syscall.Handle) string {
	if root == syscall.HKEY_CURRENT_USER {
		return "HKCU"
	}
	return "HKLM"
}

// regDWORD reads a DWORD; ok is false when it is not there. Any other error,
// or a value of another type, fails the test: that is not an answer.
func regDWORD(t *testing.T, root syscall.Handle, path, name string) (uint32, bool) {
	t.Helper()
	typ, b, err := regRead(root, path, name)
	if errors.Is(err, errNoValue) {
		return 0, false
	}
	if err != nil {
		t.Fatalf(`reading %s\%s\%s: %v`, rootName(root), path, name, err)
	}
	if typ != syscall.REG_DWORD || len(b) < 4 {
		t.Fatalf(`%s\%s\%s is type %d, %d bytes; want a DWORD`, rootName(root), path, name, typ, len(b))
	}
	return binary.LittleEndian.Uint32(b), true
}

// regString reads a REG_SZ or REG_EXPAND_SZ; ok is false when it is not there.
func regString(t *testing.T, root syscall.Handle, path, name string) (string, bool) {
	t.Helper()
	typ, b, err := regRead(root, path, name)
	if errors.Is(err, errNoValue) {
		return "", false
	}
	if err != nil {
		t.Fatalf(`reading %s\%s\%s: %v`, rootName(root), path, name, err)
	}
	if typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ {
		t.Fatalf(`%s\%s\%s is type %d; want a string`, rootName(root), path, name, typ)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return syscall.UTF16ToString(u), true
}

// wantDWORD fails t unless the value is there and equal to want, saying what
// sets it; and records what it found either way.
func wantDWORD(t *testing.T, root syscall.Handle, path, name string, want uint32, setBy string) {
	t.Helper()
	got, ok := regDWORD(t, root, path, name)
	switch {
	case !ok:
		evidence(t, `%s\%s\%s not set`, rootName(root), path, name)
		t.Fatalf(`%s\%s\%s is not set; want %d (%s)`, rootName(root), path, name, want, setBy)
	case got != want:
		evidence(t, `%s=%d`, name, got)
		t.Fatalf(`%s\%s\%s is %d; want %d (%s)`, rootName(root), path, name, got, want, setBy)
	default:
		evidence(t, `%s=%d`, name, got)
	}
}

// command runs a program and returns what it printed, stdout and stderr
// together, failing t if it could not be run or exited non-zero.
func command(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return string(out)
}

// powershell runs a script and returns its trimmed output, failing t if it
// failed. The script stops at its first error.
func powershell(t *testing.T, script string) string {
	t.Helper()
	return strings.TrimSpace(command(t, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", "$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; "+script))
}

// openWindow creates a glaze window on the main thread, calls load there
// before the run loop starts, and returns once the loop is running. Cleanup
// terminates the loop and destroys the window.
func openWindow(t *testing.T, load func(glaze.WebView)) glaze.WebView {
	t.Helper()
	return openWindowSized(t, 560, 300, load)
}

// openWindowSized is openWindow at w x h.
func openWindowSized(t *testing.T, width, height int, load func(glaze.WebView)) glaze.WebView {
	t.Helper()
	var w glaze.WebView
	created := make(chan error, 1)
	exited := make(chan struct{})
	mainQueue <- func() {
		defer close(exited)
		var err error
		if w, err = glaze.NewWithOptions(glaze.Options{}); err != nil {
			created <- err
			return
		}
		defer w.Destroy()
		w.SetTitle("irgo VM conformance: " + t.Name())
		w.SetSize(width, height, glaze.HintNone)
		load(w)
		created <- nil
		w.Run()
	}
	if err := <-created; err != nil {
		<-exited
		t.Fatalf("glaze.NewWithOptions: %v", err)
	}
	t.Cleanup(func() {
		w.Terminate()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Errorf("the window's run loop was still running 10s after Terminate")
		}
	})
	return w
}
