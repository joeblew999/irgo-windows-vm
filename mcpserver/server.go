package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joeblew999/irgo-windows-vm/command"
)

// Deps is what the server needs from the program that wires it.
//
// An injected function rather than an import, because the handlers live in
// package main and nothing can import that. It is also the boundary that keeps
// the rule in this package's doc comment honest: the server cannot run anything
// the CLI does not already run, because running is not its to do.
type Deps struct {
	// Version is what the binary reports, so a client can tell which build it
	// is talking to.
	Version string

	// Run executes a declared command with the arguments a user would have
	// typed, and returns everything it printed.
	//
	// The output matters as much as the error. A command's progress lines are
	// what an operator reads to know what happened, and they are exactly what
	// an agent needs — over stdio they cannot go to stdout, which is the
	// protocol channel, so they come back here instead. See utmvm.Capture.
	Run func(ctx context.Context, name string, args []string) (output string, err error)

	// Classify turns an error from Run into the code the CLI would have exited
	// with. Injected for the same reason as Run: the error sentinels live with
	// the code that raises them.
	Classify func(error) command.Code
}

// argsSchema is the input every tool takes: the command line, as an array.
//
// One schema for all of them, deliberately. The alternative is a typed schema
// per command, which means the flags each command accepts are declared a second
// time — once for the CLI's flag.FlagSet and once here — and the two would
// disagree the first time a default changed. This repository has spent its
// history deleting second copies of lists.
//
// The cost is real and worth naming: an agent gets less help from the schema
// and has to read the description to know the flags. That is what the generated
// MCP page and the captured -h text are for. Moving flags into package command
// so both could be generated from one declaration is the change that would fix
// it properly, and it is not this commit.
func argsSchema(name string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"args": {
				Type:        "array",
				Items:       &jsonschema.Schema{Type: "string"},
				Description: fmt.Sprintf("Command line arguments for %s, one element per token, exactly as they would be typed. Run `irgo-winvm %s -h` for the flags.", name, name),
			},
		},
	}
}

// toolInput is what a handler unmarshals a call into.
type toolInput struct {
	Args []string `json:"args"`
}

// New returns the server, with one tool per declared command.
//
// Generated from command.All rather than listed here: a tool list written out
// in this file would be a second enumeration of the commands, and it would be
// the one that silently lacks whatever gets added next.
func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "irgo-winvm",
		Title:   "Windows 11 ARM64 VM on Apple Silicon",
		Version: d.Version,
		Description: "Build a Go program on a Mac and run it on real Windows. " +
			"Answers whether a desktop build actually works on Windows, which cannot be " +
			"determined by reading the code from macOS.",
	}, nil)

	for _, c := range command.All {
		if !c.OverMCP {
			continue
		}
		s.AddTool(tool(c), handler(c.Name, d))
	}
	return s
}

// tool describes one command to a client.
func tool(c command.Command) *mcp.Tool {
	// Annotations are always serialized — the Go types are bare bools for
	// ReadOnlyHint and IdempotentHint, so omitting one still ships `false`.
	// That means there is no such thing as leaving them unset, and a wrong
	// default is a claim rather than a silence: an agent told a delete is
	// read-only will call it to find out what is there.
	destructive := c.Destructive
	a := &mcp.ToolAnnotations{
		Title:           c.Name,
		ReadOnlyHint:    c.ReadOnly,
		DestructiveHint: &destructive,
	}
	return &mcp.Tool{
		Name:        c.Name,
		Description: c.Summary,
		Annotations: a,
		InputSchema: argsSchema(c.Name),
	}
}

// handler runs one command and returns what it printed.
func handler(name string, d Deps) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in toolInput
		// Server.AddTool does not validate against the schema — that is the
		// generic AddTool's job, and it infers the schema from a Go type
		// instead of from the command list. So the unmarshal is the validation,
		// and a malformed call is reported to the model rather than raised as a
		// protocol error: it is something the model can correct.
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return errorResult(fmt.Sprintf("could not read the arguments for %s: %v", name, err)), nil
			}
		}

		out, err := d.Run(ctx, name, in.Args)
		if err != nil {
			// The output is returned alongside the error, not instead of it.
			// What a command printed before it failed is usually the answer to
			// why, and an agent that only gets "exit status 1" has to guess.
			return failure(name, out, err, d.Classify), nil
		}
		return textResult(out), nil
	}
}

func textResult(s string) *mcp.CallToolResult {
	if s == "" {
		// A tool that returns nothing reads as a tool that did nothing.
		s = "(the command printed nothing)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// errorResult reports a failure the model should see and can act on.
//
// Not a Go error: the SDK's own documentation is explicit that errors
// originating in a tool belong inside the result, or the model cannot see them
// and correct itself, and that protocol errors are for exceptional conditions.
// A missing VM is something an agent fixes by creating one, not a transport
// failure.
// outcome is the machine-readable half of a failed result.
//
// An agent must not have to parse English to decide what to do next. The
// wording of "the VM is there, the guest agent is not answering" will change;
// `"code": 4, "retryable": true` will not.
type outcome struct {
	Command   string `json:"command"`
	Code      int    `json:"code"`
	Status    string `json:"status"`
	Meaning   string `json:"meaning"`
	Retryable bool   `json:"retryable"`
}

// failure builds the result for a command that did not succeed.
//
// Every one of these is a result rather than a protocol error, including "that
// VM does not exist" — it is something an agent fixes by running vm-create, not
// a transport failure. See errorResult.
func failure(name, out string, err error, classify func(error) command.Code) *mcp.CallToolResult {
	code := command.CodeFailed
	if classify != nil {
		code = classify(err)
	}
	o, _ := command.Classify(code)

	text := fmt.Sprintf("%s failed: %v\n%s", name, err, o.Meaning)
	if o.Retryable {
		// Said in the text as well as the field, because this is the one an
		// agent gets wrong in a way that wastes a 45-minute install: give up on
		// a working VM, or retry forever against one that is not there.
		text += "\nThis one is worth retrying: Windows Update takes the guest agent away for minutes at a time and the VM is fine."
	}
	if out != "" {
		text = out + "\n" + text
	}

	r := errorResult(text)
	r.StructuredContent = outcome{
		Command:   name,
		Code:      int(o.Code),
		Status:    o.Name,
		Meaning:   o.Meaning,
		Retryable: o.Retryable,
	}
	return r
}

func errorResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}
}

// Serve runs the server on stdin and stdout until the client disconnects.
//
// StdioTransport is the convention clients spawn a server with, and the only
// one phase A needs. It is also why utmvm.Out exists: this process's stdout is
// the protocol, so a command's progress cannot go there.
func Serve(ctx context.Context, d Deps) error {
	return New(d).Run(ctx, &mcp.StdioTransport{})
}
