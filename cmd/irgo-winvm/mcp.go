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
	fs.String("http", "", "serve over HTTP on this address instead of stdin and stdout; a bare :port means 127.0.0.1. Read "+utmvm.ThreatModelURL+" first")
	fs.Bool("allow-remote", false, "bind a non-loopback address; requires IRGO_WINVM_TOKEN. Read "+utmvm.ThreatModelURL)
	return fs
}

// mcpAbout is `mcp -h`: how to register the server with a client, then what
// the server tells the agent once it is connected. The site's MCP page and
// command reference capture it from here.
var mcpAbout = `  Serves these commands to an AI agent over the Model Context Protocol. With
  no flags it speaks JSON-RPC on stdin and stdout, which is how a client
  starts it. Register it once:

    Claude Code    claude mcp add irgo-winvm -- irgo-winvm mcp
    other clients  {"mcpServers": {"irgo-winvm": {"command": "irgo-winvm", "args": ["mcp"]}}}
                   Claude Desktop reads that from
                   ~/Library/Application Support/Claude/claude_desktop_config.json

  A client that does not see your shell's PATH needs the full path as the
  command: ` + "`command -v irgo-winvm`" + ` prints it. -http serves the same tools
  over HTTP: read ` + utmvm.ThreatModelURL + ` first.

  What the server tells the agent when it connects:

` + indentWrap(mcpserver.Instructions, "    ", 78) + "\n\n"

// indentWrap wraps each paragraph of s at width columns, every line indented.
func indentWrap(s, indent string, width int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		line := indent
		for _, w := range strings.Fields(para) {
			if len(line) > len(indent) && len(line)+1+len(w) > width {
				out = append(out, line)
				line = indent
			}
			if len(line) > len(indent) {
				line += " "
			}
			line += w
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

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
		Run: func(ctx context.Context, name string, args []string) (string, error) {
			return utmvm.Capture(func() error { return runToolFor(mcpserver.ClientName(ctx), name, args) })
		},
		StartJob: func(ctx context.Context, name string, args []string) (string, error) {
			// Asked at call time so a second mutation hears "busy" now rather
			// than after forking; the job child still takes the locks itself.
			// Taken and released rather than queried, so the refusal names the
			// busy lock. An unanswerable check refuses.
			c, ok := find(name)
			if !ok {
				return "", fmt.Errorf("%w: no such command %q", errUsage, name)
			}
			v, _, err := c.parse(args)
			if err != nil {
				return "", err
			}
			v.caller = callerFor(v, mcpserver.ClientName(ctx))
			if err := admit(c, v); err != nil {
				return "", err
			}
			release, err := utmvm.Acquire(locksFor(c.Command, v)...)
			if err != nil {
				return "", err
			}
			release()
			s, err := job.Start(name, jobArgs(v, args), mcpserver.ClientName(ctx))
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
func screenshotForMCP(ctx context.Context, args []string) ([]byte, string, error) {
	// -promote photographs nothing, so there is no image to return.
	for _, a := range args {
		if a == "-promote" || strings.HasPrefix(a, "-promote=") {
			out, err := utmvm.Capture(func() error { return runToolFor(mcpserver.ClientName(ctx), "vm-screen", args) })
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

	out, err := utmvm.Capture(func() error { return runToolFor(mcpserver.ClientName(ctx), "vm-screen", args) })
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

// jobArgs is the command line a job child runs with. The child is a new
// process with no MCP client, so it is told who it runs for with -owner;
// otherwise a job would record the person at the terminal as the owner of the
// VM an agent asked for, and admit it to the owner's VM.
func jobArgs(v values, args []string) []string {
	if v.fs == nil || v.fs.Lookup("owner") == nil || v.String("owner") != "" {
		return args
	}
	return append([]string{"-owner=" + v.caller.ID}, args...)
}
