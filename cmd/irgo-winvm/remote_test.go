package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeblew999/irgo-windows-vm/internal/command"
	"github.com/joeblew999/irgo-windows-vm/internal/remote"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
	"github.com/joeblew999/irgo-windows-vm/wire"
)

// Negative control (by hand, 1 Oct 2026): making macOnly return nil fails
// every Linux and Windows row; dropping the remote pointer from its message
// fails the wording check. Restored.
func TestMacOnlyCommandsRefuseElsewhereAndPointAtRemote(t *testing.T) {
	var mac []string
	for _, c := range command.All {
		for _, goos := range []string{"linux", "windows", "darwin"} {
			err := macOnly(c, goos)
			switch {
			case !c.MacOnly && err != nil:
				t.Errorf("%s on %s refused, and it is not Mac-only: %v", c.Name, goos, err)
			case c.MacOnly && goos == "darwin" && err != nil:
				t.Errorf("%s refused on macOS: %v", c.Name, err)
			case c.MacOnly && goos != "darwin":
				if exitCode(err) != command.CodeUsage || !strings.Contains(fmt.Sprint(err), "remote submit") {
					t.Errorf("%s on %s: %v (exit %d), want a usage refusal pointing at remote submit", c.Name, goos, err, exitCode(err))
				}
			}
		}
		if c.MacOnly {
			mac = append(mac, c.Name)
		}
	}
	// The ones that drive UTM must be in the list; the client must not be.
	for _, name := range []string{"vm-create", "app-create", "vm-delete", "serve", "vm-screen"} {
		if c, _ := command.Find(name); !c.MacOnly {
			t.Errorf("%s drives UTM and is not MacOnly", name)
		}
	}
	for _, name := range []string{"remote-submit", "remote-status", "remote-logs", "remote-result", "remote-cancel", "doctor", "version"} {
		if c, _ := command.Find(name); c.MacOnly {
			t.Errorf("%s works on any OS and is marked MacOnly", name)
		}
	}
	if len(mac) == 0 {
		t.Fatal("no command is MacOnly; the check above proved nothing")
	}
}

// The refusal runs through runTool, after the flags parse (so -h still
// answers on Linux) and before anything is touched.
func TestRunToolRefusesMacOnlyOffMac(t *testing.T) {
	if err := macOnly(command.Command{Name: "x", MacOnly: true}, "linux"); !errors.Is(err, errUsage) {
		t.Fatalf("macOnly: %v", err)
	}
}

func TestRemoteSpelling(t *testing.T) {
	for in, want := range map[string]string{
		"remote submit -gui a.exe": "remote-submit -gui a.exe",
		"remote status abc":        "remote-status abc",
		"remote logs -f abc":       "remote-logs -f abc",
		"remote nothing":           "remote nothing", // not a remote command: left alone, so it is unknown
		"remote -h":                "remote -h",
		"remote-submit a.exe":      "remote-submit a.exe",
		"vm-create -vm x":          "vm-create -vm x",
	} {
		if got := strings.Join(remoteSpelling(strings.Fields(in)), " "); got != want {
			t.Errorf("%q became %q, want %q", in, got, want)
		}
	}
}

// Negative control (by hand, 1 Oct 2026): dropping the JobError case from
// exitCode makes a failed job exit 1 whatever its code, and fails the
// not-run and busy rows. Restored.
func TestAJobsCodeIsTheExitCode(t *testing.T) {
	for _, c := range []command.Code{command.CodeOK, command.CodeFailed, command.CodeNoAgent, command.CodeBusy, command.CodeNotRun} {
		n := int(c)
		j := remote.Job{ID: "x", State: wire.JobFinished, ExitCode: &n}
		if got := exitCode(remote.Err(j)); got != c {
			t.Errorf("a job that exited %d on the Mac exits %d here", c, got)
		}
	}
	lost := remote.Job{ID: "x", State: wire.JobLost}
	if got := exitCode(fmt.Errorf("wrapped: %w", remote.Err(lost))); got != command.CodeNotRun {
		t.Errorf("a lost job exits %d, want %d", got, command.CodeNotRun)
	}
	if got := exitCode(remoteErr(fmt.Errorf("x: %w", remote.ErrAuth))); got != command.CodeUsage {
		t.Errorf("a refused token exits %d, want %d", got, command.CodeUsage)
	}
}

// remote-result -admin finds another caller's job in the admin's list and
// reads its files through the admin route with IRGO_REMOTE_ADMIN_TOKEN
// alone, exiting with the job's code; a job not in the list is a usage
// error. The fake answers only the admin token, on only the admin's routes.
//
// Negative control (by hand, 1 Oct 2026): passing admin=false to
// fetchAndReport from runRemoteResult fails this with exit 2 (the caller's
// route, refused); restored.
func TestRemoteResultAdminReadsAnotherCallersJob(t *testing.T) {
	id := strings.Repeat("ab", 16)
	one := 1
	job := wire.JobView{Job: wire.Job{ID: id, Owner: "bob", State: wire.JobFinished, ExitCode: &one, Outcome: "failed",
		Files: []wire.BlobInfo{{Key: "stdout.txt", Size: 3}}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, p, _, ok := wire.Match(r.Method, r.URL.Path)
		switch {
		case !ok || r.Header.Get("Authorization") != "Bearer adm" || route.Scope != wire.ScopeJobsAdmin:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"refused: no valid bearer token","code":"unauthorized"}`))
		case route.Name == wire.RouteJobList:
			_ = json.NewEncoder(w).Encode(wire.JobList{Jobs: []wire.JobView{job}})
		case p[0] == id && p[1] == "stdout.txt":
			_, _ = w.Write([]byte("hi\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(wire.EnvRemoteURL, srv.URL)
	t.Setenv("IRGO_REMOTE_TOKEN", "")
	t.Setenv("IRGO_REMOTE_ADMIN_TOKEN", "adm")
	dir := t.TempDir()

	var err error
	_, _ = utmvm.Capture(func() error { err = run([]string{"remote-result", "-admin", "-o", dir, id}); return nil })
	if got := exitCode(err); got != command.CodeFailed {
		t.Fatalf("remote-result -admin: exit %d (%v), want the job's %d", got, err, command.CodeFailed)
	}
	if b, rErr := os.ReadFile(filepath.Join(dir, id, "stdout.txt")); rErr != nil || string(b) != "hi\n" {
		t.Fatalf("stdout.txt: %q %v", b, rErr)
	}
	_, _ = utmvm.Capture(func() error { err = run([]string{"remote-result", "-admin", "-o", dir, strings.Repeat("cd", 16)}); return nil })
	if got := exitCode(err); got != command.CodeUsage || !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("a job not in the list: exit %d (%v), want %d and not found", got, err, command.CodeUsage)
	}
}

func TestGuestArgs(t *testing.T) {
	app := remote.Job{Spec: remote.Spec{Kind: remote.KindApp, TimeoutS: 600, Args: []string{"-out={out}", "x"}}}
	if got := strings.Join(guestArgs(app), " "); got != `-out=`+guestOut+` x` {
		t.Errorf("app args: %q", got)
	}
	test := remote.Job{Spec: remote.Spec{Kind: remote.KindTest, TimeoutS: 600, Args: []string{"-test.v", "-test.run", "TestClipboard"}}}
	got := strings.Join(guestArgs(test), " ")
	if got != "-test.v=test2json -test.timeout=9m30s -test.run TestClipboard" {
		t.Errorf("test args: %q", got)
	}
	own := remote.Job{Spec: remote.Spec{Kind: remote.KindTest, TimeoutS: 600, Args: []string{"-test.timeout=1m"}}}
	if got := strings.Join(guestArgs(own), " "); got != "-test.v=test2json -test.timeout=1m" {
		t.Errorf("a test's own timeout: %q", got)
	}
}

// Negative control (by hand, 1 Oct 2026): dropping the ".." case lets
// "../../Windows/x.png" through and fails this. Restored.
func TestShotPathsTakesOnlyPlainNamesUnderOut(t *testing.T) {
	out := "=== RUN TestTray\n\x16    shots_test.go:40: screenshot: windows/TestTray.png\n" +
		"screenshot: windows/TestTray.png\n" + // twice: once
		"screenshot: ../../Windows/system.png\n" +
		"screenshot: C:\\Windows\\x.png\n" +
		"screenshot: /etc/x.png\n" +
		"screenshot: windows/notes.txt\n" +
		"screenshot: windows/TestMenu/sub test#1.png\n"
	got := strings.Join(shotPaths([]byte(out)), ",")
	if got != "windows/TestTray.png,windows/TestMenu/sub test#1.png" {
		t.Fatalf("shotPaths: %q", got)
	}
	if n := shotName("windows/TestMenu/sub test#1.png"); n != "shot-windows-TestMenu-sub_test_1.png" {
		t.Fatalf("shotName: %q", n)
	}
}
