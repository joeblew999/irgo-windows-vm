package main

import (
	"flag"
	"strings"

	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func statusFlags() *flag.FlagSet { return flag.NewFlagSet("status", flag.ContinueOnError) }

const statusAbout = `  status          every job, newest first
  status <id>     one job

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
