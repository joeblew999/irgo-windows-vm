package main

// Where each command's flags are declared, once.
//
// They were declared inside the run functions, which was right until the MCP
// server needed to describe them: a tool taking `args: []string` makes an agent
// read prose to learn what a command accepts.
//
// The FlagSet is the source, and the schema is generated from it. The other way
// round — declaring flags as data and building a FlagSet from that — makes the
// data the source and the FlagSet a copy of it, so the two can disagree and
// something has to notice. This way they cannot disagree at all: there is one
// registration, and both the command line and the JSON schema read it.
//
// Defaults and help text are never retyped for the same reason. `DefValue` and
// `Usage` come off the flag itself, so the schema cannot claim a default the
// CLI does not have.

import (
	"flag"
	"time"

	"github.com/joeblew999/irgo-windows-vm/utmvm"
)

// values reads parsed flags back by name.
//
// The run functions used to hold the *string and *bool that Flag returns. They
// cannot now, because registration happens in a different function, so this is
// how a value is read. It panics on an unknown name deliberately: that is a
// typo in this package, not a user error, and returning a zero value would make
// a flag silently stop working.
type values struct{ fs *flag.FlagSet }

func (v values) lookup(name string) flag.Value {
	f := v.fs.Lookup(name)
	if f == nil {
		panic("no flag named " + name + " on " + v.fs.Name())
	}
	return f.Value
}

func (v values) String(name string) string { return v.lookup(name).String() }
func (v values) Bool(name string) bool     { return v.lookup(name).String() == "true" }

func (v values) Duration(name string) time.Duration {
	d, err := time.ParseDuration(v.lookup(name).String())
	if err != nil {
		// Unreachable: the flag package rejected anything unparseable at Parse.
		panic("flag " + name + " is not a duration: " + v.lookup(name).String())
	}
	return d
}

// vmScreenFlags declares what vm-screen accepts.
func vmScreenFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-screen", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	fs.String("o", "", "where to write the PNG (default: the shots directory)")
	fs.String("promote", "", "copy the newest shot of each stage into this directory, named for the stage")
	return fs
}

func vmCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-create", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	fs.Bool("install", false, "run the unattended Windows install (about 45 minutes)")
	fs.Duration("timeout", 60*time.Minute, "overall limit for the install")
	return fs
}

func vmDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("vm-delete", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name")
	fs.Bool("force", false, "actually delete; without this it only lists")
	return fs
}

func appCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("app-create", flag.ContinueOnError)
	fs.Duration("timeout", 10*time.Minute, "how long to allow the guest command")
	fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	fs.Bool("gui", false, "run on the guest's desktop (required for anything with a window)")
	fs.String("user", "dev", "guest account for -gui")
	fs.Bool("detach", false, "leave it running and return, instead of waiting for it to exit")
	return fs
}

func appDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("app-delete", flag.ContinueOnError)
	fs.String("vm", utmvm.DefaultVMName, "VM name or UUID")
	return fs
}

func isoCreateFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("iso-create", flag.ContinueOnError)
	// The size is asked for, not typed in: it is a constant in utmvm, and this
	// usage string carrying a second copy is how one of them goes stale.
	fs.Bool("fetch", false, "download from Microsoft ("+utmvm.ISODownloadSize()+") if nothing local works")
	return fs
}

func isoDeleteFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("iso-delete", flag.ContinueOnError)
	fs.Bool("force", false, "actually delete; without this it only lists")
	fs.Bool("all", false, "also delete the .esd, the one thing that cannot be rebuilt")
	return fs
}

func mcpFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.Bool("list", false, "print the tools as JSON and exit, instead of serving")
	return fs
}

// flagSets is every command that takes flags.
//
// Keyed by name, which is a new way to be wrong — the same shape as `handlers`,
// and gated the same way in both directions: a command with flags and no entry
// here, and an entry naming nothing declared.
var flagSets = map[string]func() *flag.FlagSet{
	"vm-screen":  vmScreenFlags,
	"vm-create":  vmCreateFlags,
	"vm-delete":  vmDeleteFlags,
	"app-create": appCreateFlags,
	"app-delete": appDeleteFlags,
	"iso-create": isoCreateFlags,
	"iso-delete": isoDeleteFlags,
	"mcp":        mcpFlags,
}
