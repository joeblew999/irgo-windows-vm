package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func isoCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("iso-create", flag.ContinueOnError)
	fs.Bool("fetch", false, "download from Microsoft ("+utmvm.ISODownloadSize()+") if nothing local works")
	return fs
}

// runISOCreate gets the Windows installer: built from a local .esd, or with
// -fetch downloaded from Microsoft. It needs no UTM, and installs the two
// programs an ISO build uses, which iso-delete removes.
func runISOCreate(v values, _ []string) error {
	fetch := v.Bool("fetch")
	say := utmvm.Printer("iso-create")
	say("media:  %s", utmvm.Home(utmvm.ISODir()))

	say("STEP 0/4  the two programs an ISO build needs")
	for _, t := range utmvm.ISOTools() {
		say("tool:   %-16s %s", t.Name, utmvm.Home(t.Where()))
		if t.Found() {
			continue
		}
		if err := t.Ensure(); err != nil {
			return err
		}
		say("  ✓ %-16s installed at %s", t.Name, utmvm.Home(t.Path))
	}

	iso, detail, skipped, err := utmvm.ISOGet(utmvm.ISOGetOptions{Fetch: fetch}, say)
	if err != nil {
		return err
	}
	if skipped {
		say("media: %s (already there — %s)", utmvm.Home(iso), detail)
		return nil
	}
	say("media: %s (%s)", utmvm.Home(iso), detail)
	return nil
}

func isoDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("iso-delete", flag.ContinueOnError)
	fs.Bool("force", false, "actually delete; without this it only lists")
	fs.Bool("all", false, "also delete the .esd, the one thing that cannot be rebuilt")
	return fs
}

// runISODelete removes the media and the ISO tools iso-create installed.
// Without -force it lists what would go and refuses. The .esd is kept unless
// -all is given, because it is the one file that cannot be rebuilt offline.
func runISODelete(v values, _ []string) error {
	force, all := v.Bool("force"), v.Bool("all")
	say := utmvm.Printer("iso-delete")

	media := presentMedia(all)
	tools := utmvm.ISOTools()

	say("STEP 1/3  media in %s", utmvm.Home(utmvm.ISODir()))
	media.list(say)
	if len(media) == 0 {
		say("          none")
	}
	if !all {
		reportKeptESD(say)
	}

	say("STEP 2/3  looking for the tools iso installed")
	installed := installedTools(tools, say)

	if len(media) == 0 && len(installed) == 0 {
		say("nothing to delete")
		say("  media would be at %s", utmvm.Home(utmvm.ISODir()))
		for _, t := range tools {
			say("  %s would be at %s", t.Name, utmvm.Home(t.Where()))
		}
		return nil
	}

	say("STEP 3/3  nothing deleted yet — this is what would go:")
	media.list(say)
	for _, t := range installed {
		say("          %-9s %s (uninstalls %s)", "tool", utmvm.Home(t.Path), t.Formula)
	}
	if !force {
		return isoDeleteRefusal(media, len(installed), all)
	}

	say("STEP 3/3  deleting")
	if err := media.remove(say); err != nil {
		return err
	}
	uninstallTools(tools, say)
	return nil
}

// reportKeptESD names the .esd when it is being kept: a silent 4.2 GB is the
// answer nobody expects.
func reportKeptESD(say func(string, ...any)) {
	if fi, err := os.Stat(utmvm.ISOSourcePath()); err == nil {
		say("          %-9s %s  (kept — pass -all to delete it)",
			utmvm.HumanBytes(fi.Size()), filepath.Base(utmvm.ISOSourcePath()))
	}
}

// installedTools reports each ISO tool and returns those that are installed.
func installedTools(tools []utmvm.ISOTool, say func(string, ...any)) []utmvm.ISOTool {
	var installed []utmvm.ISOTool
	for _, t := range tools {
		if !t.Found() {
			say("          not installed: %s", t.Name)
			continue
		}
		say("          found %-16s %s", t.Name, utmvm.Home(t.Path))
		installed = append(installed, t)
	}
	return installed
}

// uninstallTools removes every ISO tool, not just those found installed: an
// undo has to run to completion from any starting point. A tool that will not
// uninstall is reported and left.
func uninstallTools(tools []utmvm.ISOTool, say func(string, ...any)) {
	for _, t := range tools {
		if t.Found() {
			say("          uninstalling %s (brew uninstall %s)", t.Name, t.Formula)
		}
		where, err := t.Remove()
		switch {
		case err != nil:
			say("          · %s left in place: %v", t.Name, err)
		case where != "":
			say("          · uninstalled %s from %s", t.Name, utmvm.Home(where))
		}
	}
}

// isoDeleteRefusal is the error iso-delete returns without -force: what would
// go, and what it would cost to get back.
func isoDeleteRefusal(media mediaFiles, tools int, all bool) error {
	var what []string
	if len(media) > 0 {
		n, size := media.total()
		what = append(what, fmt.Sprintf("%d file(s), %s", n, utmvm.HumanBytes(size)))
	}
	if tools > 0 {
		what = append(what, fmt.Sprintf("%d tool(s)", tools))
	}
	msg := strings.Join(what, " and ")
	switch {
	case len(media) == 0:
	case all:
		msg += "\n  Includes the .esd: " + utmvm.ISODownloadSize() + " to re-fetch from a source that rate-limits."
	default:
		// 40s is measured; see docs/RESULTS.md.
		msg += "\n  The .esd is kept, so iso-create rebuilds this in about 40s with\n" +
			"  no network. Add -all to delete that too."
	}
	return fmt.Errorf("%s\n  Pass -force to do it (%w)", msg, errRefused)
}

// mediaFile is a file in the media directory that iso-delete would remove.
type mediaFile struct {
	path string
	size int64
}

// sidecar reports whether f is a 32-byte cached scan result. Sidecars are
// deleted with the ISO they describe but never listed or counted, or one ISO
// would read as two files.
func (f mediaFile) sidecar() bool { return strings.HasSuffix(f.path, ".scan") }

type mediaFiles []mediaFile

// presentMedia returns the media files that exist: the rebuildable ones, and
// with all the .esd too.
func presentMedia(all bool) mediaFiles {
	wanted := utmvm.ISODerived()
	if all {
		wanted = utmvm.ISOFiles()
	}
	var out mediaFiles
	for _, p := range wanted {
		if fi, err := os.Stat(p); err == nil {
			out = append(out, mediaFile{p, fi.Size()})
		}
	}
	return out
}

// list prints one line per file, sidecars omitted.
func (m mediaFiles) list(say func(string, ...any)) {
	for _, f := range m {
		if !f.sidecar() {
			say("          %-9s %s", utmvm.HumanBytes(f.size), filepath.Base(f.path))
		}
	}
}

// total is the count and size of the files list prints.
func (m mediaFiles) total() (n int, size int64) {
	for _, f := range m {
		if !f.sidecar() {
			n++
			size += f.size
		}
	}
	return n, size
}

// remove deletes every file, sidecars included. The media is protected with
// the immutable flag, which blocks unlink, so each is unprotected first.
func (m mediaFiles) remove(say func(string, ...any)) error {
	for _, f := range m {
		say("          clearing the immutable flag on %s", utmvm.Home(f.path))
		// Off macOS there is no flag and this always fails, so it is not fatal
		// alone; but when the remove then fails it is the likely cause.
		uErr := utmvm.ISOUnprotect(f.path)
		if err := os.Remove(f.path); err != nil {
			if uErr != nil {
				return fmt.Errorf("%w (clearing its immutable flag failed first: %v)", err, uErr)
			}
			return err
		}
		say("  · deleted %s", utmvm.Home(f.path))
	}
	return nil
}
