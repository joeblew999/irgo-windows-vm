package main

import (
	"flag"
	"strings"
	"time"

	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func statusFlags() *flag.FlagSet { return flag.NewFlagSet("status", flag.ContinueOnError) }

const statusAbout = `  status          every VM with its owner and last use, then every job
  status <id>     one job

  Several callers can share this Mac, so the VMs come first: whose each one
  is, when it was last used, and whether vm-reap would take it.

  Jobs are long-running commands started detached — vm-create -install
  and iso-create -fetch. Started over MCP they outlive the client that
  asked for them, so this is how you find out what happened.
`

// runStatus reports detached jobs: one by id, or all of them. Whether a job is
// running comes from the operating system, not from its record.
func runStatus(_ values, args []string) error {
	say := utmvm.Reporter("status")

	if len(args) == 1 {
		s, err := job.Status(args[0])
		if err != nil {
			return err
		}
		reportJob(say, s)
		return nil
	}

	reportVMs(say)
	say("")

	all, err := job.All()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		say("no jobs have been started")
		say("jobs live in %s", utmvm.Home(job.Dir()))
		return nil
	}
	for _, s := range all {
		reportJob(say, s)
	}
	say("%d job(s), in %s", len(all), utmvm.Home(job.Dir()))
	return nil
}

func reportJob(say func(string, ...any), s job.State) {
	state := "finished"
	if s.Alive {
		state = "running"
	}
	say("%-28s %-9s %-8s %s %s", s.ID, state, s.Elapsed, s.Command, strings.Join(s.Args, " "))
}

// reportVMs lists every VM UTM knows, with its owner and last use from its
// record, and every record whose VM is gone. A VM with no record has no known
// owner: irgo-win11 is the machine owner's, the golden image is shared, and
// anything else was made before records existed.
func reportVMs(say func(string, ...any)) {
	entries, lErr := utmvm.List()
	records, bad, rErr := utmvm.VMRecords()
	byName := map[string]utmvm.VMRecord{}
	for _, r := range records {
		byName[strings.ToLower(r.Name)] = r
	}
	now := time.Now()
	say("%-20s %-8s %-8s %-40s %-17s %s", "VM", "STATE", "OS", "OWNER", "LAST USED", "IDLE")
	if lErr != nil {
		say("cannot list UTM's VMs: %v", lErr)
	}
	for _, e := range entries {
		// A VM with no record is Windows: every VM made before records said.
		owner, last, idle, os := "(no record)", "", "", utmvm.GuestWindows
		switch {
		case strings.EqualFold(e.Name, utmvm.DefaultVMName):
			owner = "(the machine's owner; reserved)"
		case strings.EqualFold(e.Name, utmvm.GoldenVMName):
			owner = "(golden image; shared)"
		}
		if r, ok := byName[strings.ToLower(e.Name)]; ok {
			owner = r.Owner
			last = r.LastUsed.Local().Format("2 Jan 15:04")
			idle = r.Idle(now).Round(time.Minute).String()
			if r.OS != "" {
				os = r.OS
			}
			delete(byName, strings.ToLower(e.Name))
		}
		say("%-20s %-8s %-8s %-40s %-17s %s", e.Name, e.Status, os, owner, last, idle)
	}
	if lErr == nil {
		for _, r := range byName {
			say("%-20s %-8s %-8s %-40s %-17s %s", r.Name, "gone", r.OS, r.Owner,
				r.LastUsed.Local().Format("2 Jan 15:04"), "record left over; vm-reap forgets it")
		}
	}
	if rErr != nil {
		say("cannot read the VM records in %s: %v", utmvm.Home(utmvm.RecordsDir()), rErr)
	}
	for _, b := range bad {
		say("unreadable record: %s", b)
	}
	say("records in %s; `irgo-winvm vm-reap` removes clones idle past their lease", utmvm.Home(utmvm.RecordsDir()))
}
