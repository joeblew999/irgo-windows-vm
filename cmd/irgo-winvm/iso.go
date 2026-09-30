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

// runISODelete removes the media, which is protected on purpose, so it says so
// rather than failing with EPERM.
func runISODelete(v values, _ []string) error {
	force, all := v.Bool("force"), v.Bool("all")
	say := utmvm.Printer("iso-delete")

	// Everything that would go, listed as what it is — things about to be
	// deleted, not an inventory. The earlier version printed the same lines
	// under "media:" and "tool:" and then refused, which read as a status
	// report followed by an unrelated complaint.
	say("STEP 1/3  media in %s", utmvm.Home(utmvm.ISODir()))
	var files []string
	var bytes int64
	wanted := utmvm.ISODerived()
	if all {
		wanted = utmvm.ISOFiles()
	}
	for _, f := range wanted {
		fi, err := os.Stat(f)
		if err != nil {
			continue // absent files are not news; the directory is named above
		}
		files = append(files, f)
		// Sidecars go with the file they describe rather than as entries of
		// their own: 32 bytes of cached scan result each, and listing them
		// separately made a two-file directory look like four.
		if strings.HasSuffix(f, ".scan") {
			continue
		}
		say("          %-9s %s", utmvm.HumanBytes(fi.Size()), filepath.Base(f))
		bytes += fi.Size()
	}
	if len(files) == 0 {
		say("          none")
	}
	// The .esd is reported even when it is not being deleted: "what is on this
	// machine" is the question, and a silent 4.2 GB is the answer nobody
	// expects.
	if !all {
		if fi, err := os.Stat(utmvm.ISOSourcePath()); err == nil {
			say("          %-9s %s  (kept — pass -all to delete it)",
				utmvm.HumanBytes(fi.Size()), filepath.Base(utmvm.ISOSourcePath()))
		}
	}
	say("STEP 2/3  looking for the tools iso installed")
	tools := utmvm.ISOTools()
	var installed []int
	for i := range tools {
		if tools[i].Found() {
			say("          found %-16s %s", tools[i].Name, utmvm.Home(tools[i].Path))
			installed = append(installed, i)
			continue
		}
		say("          not installed: %s", tools[i].Name)
	}

	if len(files) == 0 && len(installed) == 0 {
		say("nothing to delete")
		say("  media would be at %s", utmvm.Home(utmvm.ISODir()))
		for i := range tools {
			say("  %s would be at %s", tools[i].Name, utmvm.Home(tools[i].Where()))
		}
		return nil
	}

	say("STEP 3/3  nothing deleted yet — this is what would go:")
	for _, f := range files {
		if strings.HasSuffix(f, ".scan") {
			continue
		}
		fi, _ := os.Stat(f)
		say("          %-9s %s", utmvm.HumanBytes(fi.Size()), filepath.Base(f))
	}
	for _, i := range installed {
		say("          %-9s %s (uninstalls %s)", "tool", utmvm.Home(tools[i].Path), tools[i].Formula)
	}

	if !force {
		var what []string
		if len(files) > 0 {
			// Sidecars are not counted: they are 32 bytes of cache and saying
			// "2 files" for one ISO plus its scan result is just wrong.
			n := 0
			for _, f := range files {
				if !strings.HasSuffix(f, ".scan") {
					n++
				}
			}
			what = append(what, fmt.Sprintf("%d file(s), %s", n, utmvm.HumanBytes(bytes)))
		}
		if len(installed) > 0 {
			what = append(what, fmt.Sprintf("%d tool(s)", len(installed)))
		}
		msg := strings.Join(what, " and ")
		if len(files) > 0 {
			if all {
				msg += "\n  Includes the .esd: " + utmvm.ISODownloadSize() + " to re-fetch from a source that rate-limits."
			} else {
				// 40s, measured — see docs/RESULTS.md. It said "about three minutes"
				// from before the figure was taken.
				msg += "\n  The .esd is kept, so iso-create rebuilds this in about 40s with\n" +
					"  no network. Add -all to delete that too."
			}
		}
		return fmt.Errorf("%s\n  Pass -force to do it (%w)", msg, errRefused)
	}

	say("STEP 3/3  deleting")
	for _, f := range files {
		say("          clearing the immutable flag on %s", utmvm.Home(f))
		// uchg blocks unlink. A failure to clear it is not fatal on its own —
		// off macOS there is no flag and this always errors — but when the
		// remove then fails with EPERM, it is the reason, and dropping it left
		// "operation not permitted" with nothing saying the flag was why.
		uErr := utmvm.ISOUnprotect(f)
		if err := os.Remove(f); err != nil {
			if uErr != nil {
				return fmt.Errorf("%w (clearing its immutable flag failed first: %v)", err, uErr)
			}
			return err // a *PathError: it names the file already
		}
		say("  · deleted %s", utmvm.Home(f))
	}
	// The tools go whether or not the media was there: an undo has to run to
	// completion from any starting point.
	for i := range tools {
		if tools[i].Found() {
			say("          uninstalling %s (brew uninstall %s)", tools[i].Name, tools[i].Formula)
		}
		where, err := tools[i].Remove()
		switch {
		case err != nil:
			say("          · %s left in place: %v", tools[i].Name, err)
		case where != "":
			say("          · uninstalled %s from %s", tools[i].Name, utmvm.Home(where))
		}
	}
	return nil
}
