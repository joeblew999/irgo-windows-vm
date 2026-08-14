package mcpserver

import (
	"context"
	"encoding/json"
	"flag"
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

	// Screenshot runs vm-screen and returns the PNG itself.
	//
	// The CLI writes a file and prints where it went, which is the right
	// answer for a person sitting at the machine and useless to an agent: it
	// cannot open a path, and over a remote transport the path is on someone
	// else's disk. So the bytes come back and go into the result as an image.
	//
	// This is the most valuable thing the server offers. From the host a stuck
	// boot and a working one are identical — that is why vm-screen exists at
	// all — and doubly so for a caller that cannot look at a screen.
	Screenshot func(ctx context.Context, args []string) (png []byte, output string, err error)

	// StartJob runs a long command detached and returns its id.
	//
	// vm-create -install is about 45 minutes; every client times out long
	// before that, so blocking means the call is abandoned while the install
	// carries on and the agent has nothing to ask about it.
	StartJob func(name string, args []string) (id string, err error)

	// Flags returns a command's flag set, or nil if it takes none.
	//
	// The schema is generated from it. Not from a declaration beside it: that
	// would make the data the source and the FlagSet a copy, and the two could
	// disagree. This way there is one registration and both the command line
	// and the JSON schema read it, so a default cannot be claimed here that the
	// CLI does not have.
	Flags func(name string) *flag.FlagSet
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
func argsSchema(name string, fs *flag.FlagSet) *jsonschema.Schema {
	props := map[string]*jsonschema.Schema{
		"args": {
			Type:        "array",
			Items:       &jsonschema.Schema{Type: "string"},
			Description: fmt.Sprintf("Positional arguments for %s — the ones that are not flags, such as the path to a .exe.", name),
		},
	}
	if fs != nil {
		fs.VisitAll(func(f *flag.Flag) {
			p := &jsonschema.Schema{Type: "string", Description: f.Usage}
			// Bools are asked, not guessed from the name: the flag package
			// answers this itself, and `-install` and `-user` look alike.
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				p.Type = "boolean"
			}
			// Defaults come off the flag, never retyped. A schema claiming a
			// default the CLI does not have is worse than no default at all.
			if f.DefValue != "" && f.DefValue != "false" {
				p.Description += fmt.Sprintf(" (default %s)", f.DefValue)
			}
			props[f.Name] = p
		})
	}
	return &jsonschema.Schema{Type: "object", Properties: props}
}

// argv turns a call's arguments back into the command line a person would have
// typed, so the same code path runs either way.
//
// Flags first, then positionals: `app-create -vm x prog.exe`. The flag package
// stops at the first non-flag, so a positional in front would silently swallow
// every flag after it.
func argv(in map[string]any, fs *flag.FlagSet) []string {
	var out []string
	if fs != nil {
		fs.VisitAll(func(f *flag.Flag) {
			v, ok := in[f.Name]
			if !ok {
				return
			}
			// -name=value, always. Bare `-flag value` splits differently for
			// bools, and this form is unambiguous for every type.
			out = append(out, fmt.Sprintf("-%s=%v", f.Name, v))
		})
	}
	if raw, ok := in["args"]; ok {
		if list, ok := raw.([]any); ok {
			for _, a := range list {
				out = append(out, fmt.Sprint(a))
			}
		}
	}
	return out
}

// readArgs turns a call's arguments into a command line.
func readArgs(name string, raw []byte, d Deps) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var fs *flag.FlagSet
	if d.Flags != nil {
		fs = d.Flags(name)
	}
	return argv(in, fs), nil
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
		var fs *flag.FlagSet
		if d.Flags != nil {
			fs = d.Flags(c.Name)
		}
		h := handler(c.Name, d)
		if c.Name == screenCommand && d.Screenshot != nil {
			h = screenHandler(d)
		}
		s.AddTool(tool(c, fs), h)
	}
	return s
}

// tool describes one command to a client.
func tool(c command.Command, fs *flag.FlagSet) *mcp.Tool {
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
		InputSchema: argsSchema(c.Name, fs),
	}
}

// handler runs one command and returns what it printed.
func handler(name string, d Deps) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Server.AddTool does not validate against the schema — that is the
		// generic AddTool's job, and it infers the schema from a Go type
		// instead of from the command list. So reading the arguments is the
		// validation, and a malformed call is reported to the model rather than
		// raised as a protocol error: it is something the model can correct.
		args, aErr := readArgs(name, req.Params.Arguments, d)
		if aErr != nil {
			return errorResult(fmt.Sprintf("could not read the arguments for %s: %v", name, aErr)), nil
		}

		// Long work is started, not waited on. Which calls are long is declared
		// on the command — see command.Command.Detach — not guessed from a
		// duration nobody measures.
		if c, ok := command.Find(name); ok && c.DetachedBy(args) && d.StartJob != nil {
			id, sErr := d.StartJob(name, args)
			if sErr != nil {
				return failure(name, "", sErr, d.Classify), nil
			}
			r := textResult(fmt.Sprintf(
				"%s started as job %s and is running in the background.\n"+
					"It outlives this connection. Call `status` with that id to see whether it is "+
					"still alive and how long it has been going, and `vm-screen` to see what it is doing.",
				name, id))
			r.StructuredContent = started{Command: name, Job: id, Running: true}
			return r, nil
		}

		out, rErr := d.Run(ctx, name, args)
		if rErr != nil {
			// The output is returned alongside the error, not instead of it.
			// What a command printed before it failed is usually the answer to
			// why, and an agent that only gets "exit status 1" has to guess.
			return failure(name, out, rErr, d.Classify), nil
		}
		return textResult(out), nil
	}
}

// screenCommand is the one command whose result is a picture.
//
// Named rather than inferred: there is no property of a command that says "this
// produces an image", and inventing one for a single case would be a field
// every other command carries to say `false`.
const screenCommand = "vm-screen"

// screenHandler returns the guest's screen as an image.
func screenHandler(d Deps) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, aErr := readArgs(screenCommand, req.Params.Arguments, d)
		if aErr != nil {
			return errorResult(fmt.Sprintf("could not read the arguments for %s: %v", screenCommand, aErr)), nil
		}
		png, out, err := d.Screenshot(ctx, args)
		if err != nil {
			return failure(screenCommand, out, err, d.Classify), nil
		}
		if len(png) == 0 {
			// -promote publishes shots already taken and photographs nothing.
			// Reporting "here is the screen" with no screen would be a lie the
			// caller cannot detect.
			return textResult(out), nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: out},
			&mcp.ImageContent{Data: png, MIMEType: "image/png"},
		}}, nil
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
// started is what a caller gets instead of a result it would have waited 45
// minutes for.
type started struct {
	Command string `json:"command"`
	Job     string `json:"job"`
	Running bool   `json:"running"`
}

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

// Describe returns what a client would see on connecting: every tool, with its
// description, annotations and input schema.
//
// It exists so the documentation can be captured from the binary rather than
// transcribed, the way the command reference already is. The site generator
// calls `irgo-winvm mcp -list` and renders this; it does not import this
// package, which would drag the protocol SDK and its eight dependencies into a
// module that requires only a markdown parser.
//
// Built from the same New() the server runs, so a page cannot describe a tool
// the server does not offer.
func Describe(d Deps) ([]*mcp.Tool, error) {
	ctx := context.Background()
	server := New(d)
	ct, st := mcp.NewInMemoryTransports()

	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, st) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "irgo-winvm-describe", Version: d.Version}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cs.Close(); <-done }()

	// Listed over the protocol rather than read out of the Go values, so what
	// is documented is what a client actually receives — including whatever the
	// SDK does to it on the way out.
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// Serve runs the server on stdin and stdout until the client disconnects.
//
// StdioTransport is the convention clients spawn a server with, and the only
// one phase A needs. It is also why utmvm.Out exists: this process's stdout is
// the protocol, so a command's progress cannot go there.
func Serve(ctx context.Context, d Deps) error {
	return New(d).Run(ctx, &mcp.StdioTransport{})
}
