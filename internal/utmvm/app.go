package utmvm

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Where pushed binaries and captured output live in a Windows guest, and where
// anything destined for the interactive session does: windowsGuest (guest.go)
// says which and why. Named here because the app stage is Windows only, and
// most of what it writes is a path under one of the two.
var (
	guestTemp   = windowsGuest.tempDir
	guestPublic = windowsGuest.publicDir
)

// GuestPublicPath is name inside guestPublic: where a program run with -gui
// can leave files for the host to Pull.
func GuestPublicPath(name string) string { return windowsGuest.publicPath(name) }

// Two prefixes for the files this package leaves in the guest, and they must
// never overlap.
//
// scratchPrefix marks what app-delete is allowed to sweep: helper files left
// behind by a run that did not finish. execPrefix marks the transport that
// app-delete itself runs on — the batch file being executed, the file its
// output is being written to, and the exit code the host is waiting for.
//
// Both were "irgo-", in the same directory, so `del C:\Windows\Temp\irgo-*`
// deleted the machinery running it. Two symptoms, neither of which pointed
// here: app-delete failed at random, because whether `del` reported an error
// depended on which of its own files were still open; and a listing printed
// `del`'s "Could Not Find C:\...\hello.exe" as though it were a filename,
// because the output file it was reading had been destroyed mid-write and what
// came back was another command's.
//
// The glob is a prefix match, so the separator matters: `irgo-*` requires a
// literal "-" in the fifth position and therefore cannot match "irgox-".
const (
	scratchPrefix = "irgo-"
	execPrefix    = "irgox-"
)

// pushScript writes a batch file, sends it to the guest, and removes the local
// copy.
//
// Everything that runs in the guest goes through a batch file rather than
// straight through exec, and that is not a style choice: cmd.exe applies its own
// quote-stripping to a quoted command line passed through `utmctl exec`, so any
// command containing quotes silently fails. Writing it to a file and running the
// file by path is the only reliable route.
//
// Three call sites did this by hand and each could drop the temp file on a
// different error path.
func pushScript(vmRef, guestPath, script string) error {
	tmp, err := os.CreateTemp("", "irgo-script-*.bat")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // scratch
	if _, err := tmp.WriteString(script); err != nil {
		_ = tmp.Close() // already failing
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return Push(vmRef, tmp.Name(), guestPath, func(string, ...any) {})
}

// batchFile builds the CRLF batch text that runs argv, capturing its output and
// exit code to files the host can pull afterwards.
//
// The capture is the point: `utmctl exec` never returns the guest's output and
// always exits 0, so a suite that ran nothing looks exactly like one that
// passed.
func batchFile(argv []string, outFile, rcFile string) string {
	return batchSteps([][]string{argv}, outFile, rcFile)
}

// batchSteps is batchFile for several commands run in order. The first that
// exits non-zero stops the rest, and its code is the one recorded.
//
// The test is `%ERRORLEVEL% neq 0`, not `if errorlevel 1`: that one means "1 or
// more", so a crash (exit code -1073741819) would count as success.
// %ERRORLEVEL% reads the previous line's code because cmd parses a batch file
// a line at a time, as it runs.
func batchSteps(cmds [][]string, outFile, rcFile string) string {
	var b strings.Builder
	b.WriteString("@echo off\r\n")
	for i, argv := range cmds {
		redir := " > "
		if i > 0 {
			redir = " >> "
		}
		b.WriteString(quoteForCmd(argv) + redir + "\"" + outFile + "\" 2>&1\r\n")
		if i < len(cmds)-1 {
			b.WriteString("if %ERRORLEVEL% neq 0 goto done\r\n")
		}
	}
	if len(cmds) > 1 {
		b.WriteString(":done\r\n")
	}
	b.WriteString("echo %ERRORLEVEL% > \"" + rcFile + "\"\r\n")
	return b.String()
}

// Push copies a local file into the guest, and says which way it went.
//
// utmctl file push moves about 0.4 MB/s — measured 30 Sep 2026: a 3-byte file
// in 0.19 s, a 6.9 MB Go binary in 17.8 s — so the time is the bytes, not the
// call. Anything over pushZipMin therefore goes over the guest's SMB share when
// it has one (pushShared), and otherwise compressed through utmctl: zipped, that
// binary is about 2.9 MB and the push takes a third of the time; bsdtar, which
// ships with Windows 11, expands it in the guest in well under a second. Small
// files, batch files and scripts, go through utmctl as they are: one call is
// cheaper than either route's extra guest round trip.
func Push(vmRef, localPath, guestPath string, say func(string, ...any)) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.Size() < pushZipMin {
		return pushRaw(vmRef, localPath, guestPath)
	}
	start := time.Now()
	ip, serr := pushShared(vmRef, localPath, guestPath)
	if serr == nil {
		say("pushed %s over SMB to %s in %s", HumanBytes(info.Size()), ip, time.Since(start).Round(10*time.Millisecond))
		return nil
	}
	say("pushing %s compressed through utmctl, because the SMB share did not work (%s); if it is missing, `irgo-winvm vm-repair` opens it",
		HumanBytes(info.Size()), firstLine(serr.Error()))
	start = time.Now()
	if err := pushZipped(vmRef, localPath, guestPath); err != nil {
		return err
	}
	say("pushed in %s", time.Since(start).Round(10*time.Millisecond))
	return nil
}

// pushZipMin is the size below which Push sends a file through utmctl as it is.
const pushZipMin = 256 << 10

// pushZipped zips localPath on the host as one entry named after guestPath,
// pushes the zip beside guestPath, and expands it there. tar checks every
// entry's CRC, so a zero exit means the bytes arrived intact.
func pushZipped(vmRef, localPath, guestPath string) error {
	dir, name := guestPath[:strings.LastIndex(guestPath, `\`)], path.Base(strings.ReplaceAll(guestPath, `\`, "/"))
	zipLocal, err := zipOne(localPath, name)
	if err != nil {
		return fmt.Errorf("compressing %s: %w", localPath, err)
	}
	defer func() { _ = os.Remove(zipLocal) }() // scratch
	zipGuest := dir + `\` + scratchPrefix + name + ".zip"
	if err := pushRaw(vmRef, zipLocal, zipGuest); err != nil {
		return err
	}
	res, err := appExec(vmRef, []string{"tar", "-xf", zipGuest, "-C", dir}, time.Minute, func(string, ...any) {})
	_, _ = Named(vmRef).Exec("cmd.exe", "/c", "del /q "+zipGuest)
	if err != nil {
		return fmt.Errorf("expanding %s in the guest: %w", zipGuest, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("expanding %s in the guest: tar exited %d: %s", zipGuest, res.ExitCode, res.Stdout)
	}
	return nil
}

// zipOne writes a temporary zip holding localPath as entry name, and returns
// its path. Close on the writers is checked: that is where a full disk shows up.
func zipOne(localPath, name string) (string, error) {
	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }() // read-only
	tmp, err := os.CreateTemp("", "irgo-push-*.zip")
	if err != nil {
		return "", err
	}
	fail := func(e error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", e
	}
	zw := zip.NewWriter(tmp)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return fail(err)
	}
	if _, err := io.Copy(w, src); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// pushRaw streams localPath into the guest as it is. utmctl's file push reads
// the payload from stdin rather than taking a source path.
func pushRaw(vmRef, localPath, guestPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only

	cmd := utmCommand(context.Background(), utmctlPath(), "file", "push", vmRef, guestPath)
	cmd.Stdin = f
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pushing %s to %s: %w: %s", localPath, guestPath, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// Pull reads a file out of the guest.
func Pull(vmRef, guestPath string) ([]byte, error) {
	cmd := utmCommand(context.Background(), utmctlPath(), "file", "pull", vmRef, guestPath)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pulling %s: %w: %s", guestPath, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// AppResult is the outcome of running something in the guest.
type AppResult struct {
	Stdout   string
	ExitCode int
}

// appExec executes a command in the guest and returns its output and exit
// code.
//
// Two properties of utmctl exec force this shape:
//
//  1. It does not stream the process's output back, and always exits 0 whatever
//     the guest command did. Treating an empty result as success is how a suite
//     that ran nothing would look like a suite that passed.
//  2. Complex command lines do not survive it. Passing
//     `cmd.exe /c "prog" > "out" 2>&1 & echo %ERRORLEVEL% > "rc"` produced
//     neither file: cmd.exe applies its own quote-stripping rules to a string
//     that already contains quotes, and the whole line silently does nothing.
//
// So the command is written to a batch file, pushed, and run by path. A batch
// file has no quoting ambiguity, and each line is parsed as it executes — which
// also makes %ERRORLEVEL% read the previous command's code rather than being
// expanded early, as it is in a single chained line.
func appExec(vmRef string, argv []string, timeout time.Duration, say func(string, ...any)) (AppResult, error) {
	return appExecSteps(vmRef, [][]string{argv}, timeout, say)
}

// appExecSteps is appExec for several commands in one batch (see batchSteps).
// A guest command costs its round trips — a push, an exec, polls and a pull,
// about a second — not its work, so related commands share one.
func appExecSteps(vmRef string, cmds [][]string, timeout time.Duration, say func(string, ...any)) (AppResult, error) {
	var res AppResult
	if len(cmds) == 0 {
		return res, fmt.Errorf("no command given")
	}
	for _, argv := range cmds {
		if len(argv) == 0 {
			return res, fmt.Errorf("no command given")
		}
	}
	if timeout == 0 {
		timeout = 10 * time.Minute
	}

	g, err := guestOf(vmRef)
	if err != nil {
		return res, err
	}

	// execPrefix, not scratchPrefix: these three are live for the duration of
	// the call, and app-delete sweeps scratchPrefix with a glob.
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	batFile := g.tempPath(execPrefix + stamp + g.scriptExt)
	outFile := g.tempPath(execPrefix + `out-` + stamp + `.txt`)
	rcFile := g.tempPath(execPrefix + `rc-` + stamp + `.txt`)

	if err := pushScript(vmRef, batFile, g.script(cmds, outFile, rcFile)); err != nil {
		return res, err
	}
	if _, err := Named(vmRef).Exec(g.runScript(batFile)...); err != nil {
		return res, err
	}

	// exec returns once the process is launched, so wait for the exit-code file
	// rather than assuming the work is done.
	rcRaw, werr := waitForGuest(vmRef, rcFile, timeout, say)
	if werr != nil {
		return res, werr
	}

	// Both checked. Swallowing these meant a failed pull or an unparsable exit
	// code returned AppResult{Stdout: "", ExitCode: 0} with err == nil — a suite
	// that ran nothing, indistinguishable from a suite that passed. Everything
	// used to verify this VM works runs through here, so it must not lie.
	out, perr := Pull(vmRef, outFile)
	if perr != nil {
		return res, fmt.Errorf("reading command output from %s: %w", vmRef, perr)
	}
	res.Stdout = string(bytes.TrimRight(out, "\r\n"))

	n, cerr := strconv.Atoi(strings.TrimSpace(string(rcRaw)))
	if cerr != nil {
		return res, fmt.Errorf("unreadable exit code %q from %s: %w", string(rcRaw), vmRef, cerr)
	}
	res.ExitCode = n

	_, _ = Named(vmRef).Exec(g.remove(batFile, outFile, rcFile)...)
	return res, nil
}

// quoteForCmd renders argv for cmd.exe, quoting only what needs it.
//
// Paths under C:\Windows\Temp are space-free, but a caller's arguments are not
// guaranteed to be, and an unquoted space silently becomes two arguments.
func quoteForCmd(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		if a == "" || strings.ContainsAny(a, ` "&|<>^`) {
			parts = append(parts, `"`+strings.ReplaceAll(a, `"`, `""`)+`"`)
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
