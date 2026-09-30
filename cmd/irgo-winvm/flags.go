package main

import (
	"flag"
	"strconv"
	"time"
)

// Each command declares its flags once, in a func beside its run func that
// returns a fresh *flag.FlagSet. The command line parses that set, and the MCP
// server generates the tool's JSON schema from the same set, so the two cannot
// disagree about names, defaults or help text.

// values reads parsed flags back by name.
//
// An unknown name panics: it is a typo in this package, and a zero value would
// make the flag silently stop working.
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

// Int64 and Duration panic on a malformed value, which Parse has already
// rejected, so it cannot happen.
func (v values) Int64(name string) int64 {
	n, err := strconv.ParseInt(v.lookup(name).String(), 10, 64)
	if err != nil {
		panic("flag " + name + " is not an integer: " + v.lookup(name).String())
	}
	return n
}

func (v values) Duration(name string) time.Duration {
	d, err := time.ParseDuration(v.lookup(name).String())
	if err != nil {
		panic("flag " + name + " is not a duration: " + v.lookup(name).String())
	}
	return d
}
