package mcpserver

// The documentation the server serves to a connected agent.
//
// # Why this is generated and not embedded
//
// The obvious answer was to embed llms-full.txt — the whole documentation as
// one file, which the site already publishes. It does not work: go:embed needs
// the file to exist when the package compiles, and that file is generated into
// site/dist, which is gitignored. `go build ./...` on a fresh clone would fail
// before the site had ever been built.
//
// Every way around that is worse than the problem. Committing the generated
// corpus makes the second copy this repository exists to delete, and it goes
// stale between builds. Committing an empty placeholder and overwriting it at
// release means a development build serves an empty corpus and says nothing
// about it. Fetching from the published site makes an offline server useless
// and ties a local tool to a Pages deploy.
//
// So this serves what the binary genuinely knows — its own commands, their
// flags and their defaults, read from the same FlagSets the command line parses
// — and links to the published corpus for the prose it does not carry. A
// smaller, honest resource rather than a large one that might be lying.

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joeblew999/irgo-windows-vm/command"
)

const (
	// referenceURI is the command reference, generated in this process.
	referenceURI = "irgo-winvm://reference"

	// corpusURL is the whole documentation, which the site publishes and this
	// binary does not carry. Named here rather than described vaguely, so an
	// agent can go and fetch it.
	corpusURL = "https://joeblew999.github.io/irgo-windows-vm/llms-full.txt"

	// referenceTTL is how long a client may cache the reference.
	//
	// The content changes only when the binary changes, and the binary cannot
	// change while it is running — so this is stale only across a restart.
	// Serving it with no hint would have clients re-fetch a fixed document.
	referenceTTL = 3600_000 // one hour, in milliseconds
)

// addResources registers what the server can tell an agent about itself.
func addResources(s *mcp.Server, d Deps) {
	s.AddResource(&mcp.Resource{
		URI:      referenceURI,
		Name:     "irgo-winvm command reference",
		Title:    "Every command, every flag, and what they default to",
		MIMEType: "text/markdown",
		Description: "Generated from this binary's own flag definitions, so no default is " +
			"transcribed. Read this before guessing at arguments.",
	}, referenceHandler(d))
}

func referenceHandler(d Deps) mcp.ResourceHandler {
	return func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		res := &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      referenceURI,
				MIMEType: "text/markdown",
				Text:     reference(d),
			}},
		}
		res.TTLMs = referenceTTL
		return res, nil
	}
}

// reference renders the commands and their flags.
//
// Read from the same FlagSets the command line parses — the schemas are built
// from those too — so this cannot describe a flag the CLI does not accept or a
// default it does not use.
func reference(d Deps) string {
	var b strings.Builder
	b.WriteString("# irgo-winvm\n\n")
	b.WriteString("Build a Go program on a Mac and run it on real Windows 11 ARM64, in a VM on\n")
	b.WriteString("this machine. Generated from the running binary: every flag and default below\n")
	b.WriteString("is read from the same definitions the command line parses.\n\n")
	b.WriteString("Version: " + d.Version + "\n\n")

	b.WriteString("## The three steps\n\n")
	b.WriteString("`iso-create` gets the Windows installer, `vm-create` makes a VM with Windows on\n")
	b.WriteString("it, `app-create` runs your .exe in that VM. Each is cheap to repeat: if it is\n")
	b.WriteString("already done it says so and stops. Each has an undo.\n\n")

	for _, c := range command.All {
		if !c.OverMCP {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", c.Name, c.Summary)
		switch {
		case c.Destructive:
			b.WriteString("**Destroys something.** Needs `-force`, which is never defaulted.\n\n")
		case c.ReadOnly:
			b.WriteString("Reports only; changes nothing.\n\n")
		}
		if c.Detach != "" {
			fmt.Fprintf(&b, "With `%s` this runs as a background job and returns a handle "+
				"immediately, because it takes far longer than any client will wait. Ask `status` "+
				"about the id it gives you.\n\n", c.Detach)
		}
		if d.Flags == nil {
			continue
		}
		fs := d.Flags(c.Name)
		if fs == nil {
			b.WriteString("No flags.\n\n")
			continue
		}
		var rows []string
		fs.VisitAll(func(f *flag.Flag) {
			def := f.DefValue
			if def == "" {
				def = "(none)"
			}
			rows = append(rows, fmt.Sprintf("| `-%s` | %s | %s |", f.Name, f.Usage, def))
		})
		if len(rows) == 0 {
			b.WriteString("No flags.\n\n")
			continue
		}
		sort.Strings(rows)
		b.WriteString("| flag | what it does | default |\n|---|---|---|\n")
		b.WriteString(strings.Join(rows, "\n") + "\n\n")
	}

	b.WriteString("## What a failure means\n\n")
	b.WriteString("Failures come back as results, not protocol errors, with a `status` and a\n")
	b.WriteString("`retryable` field. Match on those, never on the wording.\n\n")
	b.WriteString("| status | code | meaning | retry? |\n|---|---|---|---|\n")
	for _, o := range command.Outcomes {
		retry := "no"
		if o.Retryable {
			retry = "**yes**"
		}
		fmt.Fprintf(&b, "| `%s` | %d | %s | %s |\n", o.Name, o.Code, o.Meaning, retry)
	}

	b.WriteString("\n## The rest of the documentation\n\n")
	b.WriteString("This binary carries its own interface and nothing else. The prose — what the\n")
	b.WriteString("project is for, what has been measured, what was found upstream, and how the\n")
	b.WriteString("code is organised — is published as one file:\n\n")
	b.WriteString(corpusURL + "\n\n")
	b.WriteString("It is not embedded here on purpose. Embedding it would mean committing a\n")
	b.WriteString("generated copy that goes stale, and a stale answer is worse than a link.\n")
	return b.String()
}
