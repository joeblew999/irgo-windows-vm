package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func appCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("app-create", flag.ContinueOnError)
	fs.Duration("timeout", 10*time.Minute, "how long to allow the guest command")
	fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	fs.Bool("gui", false, "run on the guest's desktop (required for anything with a window)")
	fs.String("user", "dev", "guest account for -gui")
	fs.Bool("detach", false, "leave it running and return, instead of waiting for it to exit")
	return fs
}

// runAppCreate pushes a local Windows binary into the VM, runs it, and prints
// what it wrote. The guest program's non-zero exit is an error naming the code.
func runAppCreate(v values, args []string) error {
	name, timeout := v.String("vm"), v.Duration("timeout")
	gui, user, detach := v.Bool("gui"), v.String("user"), v.Bool("detach")
	if name == "" || len(args) == 0 {
		return fmt.Errorf("%w: irgo-winvm app-create -vm <name> <local.exe> [args...]", errUsage)
	}
	say := utmvm.Printer("app-create")
	e, err := utmvm.Find(name)
	if err != nil {
		return err
	}
	if err := ensureAgent(e, say); err != nil {
		return err
	}

	local := args[0]
	say("vm:     %s", e.Name)
	say("binary: %s", local)
	res, err := utmvm.AppCreate(e.UUID, local, utmvm.AppOptions{
		Args:    args[1:],
		GUI:     gui,
		User:    user,
		Detach:  detach,
		Timeout: timeout,
		Say:     say,
	})
	// The guest's output goes through the printer, so the log holds the answer
	// as well as every step before it.
	if res.Stdout != "" {
		for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
			say("%s", line)
		}
	}
	if err != nil {
		return err
	}
	// The hints are printed here because only this layer knows the VM's name;
	// utmvm sees the UUID.
	if detach {
		say("watch it with:    irgo-winvm vm-screen -vm %s", e.Name)
		say("take it off with: irgo-winvm app-delete -vm %s %s", e.Name, filepath.Base(local))
		return nil
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s exited %d in the guest", filepath.Base(local), res.ExitCode)
	}
	return nil
}

func appDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("app-delete", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	return fs
}

// runAppDelete removes what app-create and app-upload left: binaries staged on
// the host, and binaries and scratch files in the guest. A missing VM means
// nothing was put on it, which is success, so the undo can run twice.
func runAppDelete(v values, args []string) error {
	name := v.String("vm")
	if name == "" {
		return fmt.Errorf("app-delete: -vm was given an empty name")
	}
	say := utmvm.Printer("app-delete")
	// The stage is on the host, so it is cleared whether or not the VM exists.
	if err := utmvm.ClearStage(); err != nil {
		return err
	}
	say("stage:  %s", utmvm.Home(utmvm.VMStageDir()))
	say("vm:     %s", name)
	say("guest:  %s and %s", `C:\Windows\Temp`, `C:\Users\Public`)
	e, err := utmvm.Find(name)
	if err != nil {
		say("UTM knows no VM %q; nothing to delete", name)
		return nil
	}
	if err := utmvm.AppDelete(e.UUID, func(f string, a ...any) { say("  "+f, a...) }, args...); err != nil {
		return err
	}
	say("cleaned %s", e.Name)
	return nil
}

func appUploadFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("app-upload", flag.ContinueOnError)
	fs.String("hash", "", "SHA-256 of the whole binary, as 64 hex digits")
	fs.Int64("total", 0, "size of the whole binary, in bytes")
	fs.Int64("offset", 0, "byte offset of this chunk in the whole binary")
	fs.String("data", "", "this chunk, base64-encoded (up to 2 MiB of binary per call)")
	return fs
}

// runAppUpload stages a binary for app-create from base64 chunks, for a remote
// MCP client with no shared filesystem. The finished file is bin/<sha256>.exe,
// and that path is what the client passes to app-create.
func runAppUpload(v values, _ []string) error {
	hash, total, offset := v.String("hash"), v.Int64("total"), v.Int64("offset")
	data, err := base64.StdEncoding.DecodeString(v.String("data"))
	if err != nil {
		return fmt.Errorf("%w: -data is not base64: %v", errUsage, err)
	}
	say := utmvm.Printer("app-upload")

	staged, n, err := utmvm.Upload(hash, total, offset, data)
	if err != nil {
		return err
	}
	if staged != "" {
		say("staged %s (%s)", utmvm.Home(staged), utmvm.HumanBytes(n))
		return nil
	}
	say("chunk accepted — %d of %d bytes staged", n, total)
	return nil
}
