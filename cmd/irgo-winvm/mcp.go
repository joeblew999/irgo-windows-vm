package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/joeblew999/irgo-windows-vm/internal/job"
	"github.com/joeblew999/irgo-windows-vm/internal/mcpserver"
	"github.com/joeblew999/irgo-windows-vm/internal/utmvm"
)

func mcpFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.Bool("list", false, "print the tools as JSON and exit, instead of serving")
	fs.String("http", "", "serve over HTTP on this address instead of stdin and stdout; a bare :port means 127.0.0.1. Read docs/THREAT-MODEL.md first")
	fs.Bool("allow-remote", false, "bind a non-loopback address; requires IRGO_WINVM_TOKEN. Read docs/THREAT-MODEL.md")
	return fs
}

const mcpAbout = `  Serves the commands above to an agent over the Model Context Protocol.
  With no flags it speaks JSON-RPC on stdin and stdout; -http serves the
  same tools over HTTP. Read docs/THREAT-MODEL.md before -http.
`

// runMCP serves every command to an agent as an MCP tool, or with -list prints
// the tool descriptions the documentation is generated from.
func runMCP(v values, _ []string) error {
	if v.Bool("list") {
		tools, err := mcpserver.Describe(mcpDeps())
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tools)
	}
	if addr := v.String("http"); addr != "" {
		return mcpserver.ServeHTTP(context.Background(), mcpserver.ServeHTTPOptions{
			Addr:        addr,
			Secret:      os.Getenv("IRGO_WINVM_TOKEN"),
			AllowRemote: v.Bool("allow-remote"),
		}, mcpDeps())
	}
	return mcpserver.Serve(context.Background(), mcpDeps())
}

// mcpDeps wires the server to this program. Every tool runs through runTool,
// the same path as the command line, so a tool cannot do anything the CLI
// cannot. Output is captured because stdout is the JSON-RPC stream; the
// captured text becomes the tool result.
func mcpDeps() mcpserver.Deps {
	return mcpserver.Deps{
		Version:    version,
		Classify:   exitCode,
		Screenshot: screenshotForMCP,
		// A fresh set on every call: a parsed set would report the last call's
		// values as the next one's defaults.
		Flags: func(name string) *flag.FlagSet {
			if c, ok := find(name); ok && c.flags != nil {
				return c.flags()
			}
			return nil
		},
		Run: func(_ context.Context, name string, args []string) (string, error) {
			return utmvm.Capture(func() error { return runTool(name, args) })
		},
		StartJob: func(name string, args []string) (string, error) {
			// Asked at call time so a second mutation hears "busy" now rather
			// than after forking. The job child still takes the lock itself.
			// An unanswerable check refuses.
			held, err := utmvm.MutationHeld()
			if err != nil {
				return "", err
			}
			if held {
				return "", utmvm.ErrMutationInProgress
			}
			s, err := job.Start(name, args)
			if err != nil {
				return "", err
			}
			return s.ID, nil
		},
	}
}

// screenshotForMCP runs vm-screen and returns the PNG's bytes, since a remote
// agent cannot open a path on this machine. The file is removed afterwards
// unless the caller passed -o and so asked for it.
func screenshotForMCP(_ context.Context, args []string) ([]byte, string, error) {
	// -promote photographs nothing, so there is no image to return.
	for _, a := range args {
		if a == "-promote" || strings.HasPrefix(a, "-promote=") {
			out, err := utmvm.Capture(func() error { return runTool("vm-screen", args) })
			return nil, out, err
		}
	}

	dst, keep := explicitOutput(args)
	if dst == "" {
		f, err := os.CreateTemp("", "irgo-mcp-shot-*.png")
		if err != nil {
			return nil, "", err
		}
		dst = f.Name()
		_ = f.Close()
		args = append(append([]string{}, args...), "-o", dst)
	}
	if !keep {
		defer func() { _ = os.Remove(dst) }()
	}

	out, err := utmvm.Capture(func() error { return runTool("vm-screen", args) })
	if err != nil {
		return nil, out, err
	}
	png, err := os.ReadFile(dst)
	if err != nil {
		return nil, out, fmt.Errorf("vm-screen reported success but wrote no readable PNG at %s: %w", dst, err)
	}
	return png, out, nil
}

// explicitOutput reports the -o the caller passed, if any.
func explicitOutput(args []string) (path string, given bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "-o="); ok {
			return v, true
		}
		if a == "-o" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
