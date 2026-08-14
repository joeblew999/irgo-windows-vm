package main

// The command reference, captured from the binary rather than written down.
//
// README deliberately lists no flags, reasoning that a hand-written list goes
// stale. That reasoning is right and it left a real gap: a reader — or an agent
// — could not construct a correct invocation from the site at all.
//
// So this runs the tool and prints what it says. Nothing here transcribes a
// flag, a default or a usage string, and it cannot: the text is whatever the
// binary emits. That matters for more than tidiness. `iso-create -fetch`
// computes its own usage string from a constant, so its "(4.2 GB)" is only
// correct if it is captured rather than copied.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// generateReference builds the CLI, asks it what it can do, and renders the
// answer as markdown.
//
// Every failure here fails the site build. A reference that quietly loses a
// command — or publishes an empty page because the capture went to the wrong
// stream — is precisely the silent loss this repository documents everywhere
// else, and it would look completely fine in a browser.
func generateReference(root string) ([]byte, error) {
	bin, cleanup, err := buildCLI(root)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// The command list comes from the binary, never from a list kept here.
	// `commands` exists for exactly this: scraping the usage text would mean a
	// layout change could silently drop a command from the reference.
	names, err := capture(bin, "commands")
	if err != nil {
		return nil, fmt.Errorf("asking the binary for its commands: %w", err)
	}
	list := strings.Fields(names)
	if len(list) == 0 {
		return nil, fmt.Errorf("the binary reported no commands at all")
	}

	var b strings.Builder
	b.WriteString("# Commands\n\n")
	b.WriteString("Every command and every flag, captured from the binary when this page\n")
	b.WriteString("was built. Nothing here is transcribed, so it cannot disagree with the\n")
	b.WriteString("tool: if a flag is wrong here, it is wrong in the binary.\n\n")

	overview, err := capture(bin, "help")
	if err != nil {
		return nil, fmt.Errorf("capturing `help`: %w", err)
	}
	b.WriteString("## The three steps\n\n```\n")
	b.WriteString(strings.TrimRight(overview, "\n"))
	b.WriteString("\n```\n\n## Every command\n\n")

	for _, name := range list {
		out, cErr := capture(bin, name, "-h")
		if cErr != nil {
			return nil, fmt.Errorf("capturing `%s -h`: %w", name, cErr)
		}
		out = strings.TrimRight(out, "\n")

		// Nothing at all is a broken capture, not a command without flags.
		//
		// This check exists because its absence was caught by a negative
		// control. The first version treated an empty capture as "no flags",
		// and dropping stderr from the capture then published a page declaring
		// that seven commands with flags had none — with -fetch's computed
		// "(4.2 GB)" gone entirely. It built, it rendered, it looked right.
		//
		// Every command answers -h with something: the ones with flags print
		// usage to stderr, and the four without print their own output to
		// stdout. So silence means the output went somewhere this did not look.
		if strings.TrimSpace(out) == "" {
			return nil, fmt.Errorf("`%s -h` produced no output on either stream; "+
				"the capture is broken, not the command", name)
		}

		// A command with no flags answers -h by doing its job — version prints
		// a version, commands prints the list. Only the flag package announces
		// itself this way, so this distinguishes the two without a list of
		// which commands have flags, which would be one more copy to go stale.
		if !strings.HasPrefix(out, "Usage of "+name+":") {
			fmt.Fprintf(&b, "### `%s`\n\nNo flags.\n\n", name)
			continue
		}
		fmt.Fprintf(&b, "### `%s`\n\n```\n%s\n```\n\n", name, out)
	}
	return []byte(b.String()), nil
}

// buildCLI compiles the tool to a temporary path and returns it.
//
// Built rather than `go run`, because the reference runs it a dozen times and
// `go run` recompiles for each. The site is a separate module, so this shells
// out from the repository root where the CLI's module lives.
func buildCLI(root string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "irgo-reference-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	bin := filepath.Join(dir, "irgo-winvm")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/irgo-winvm")
	cmd.Dir = root
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if bErr := cmd.Run(); bErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("building the CLI for the reference: %w: %s",
			bErr, strings.TrimSpace(errb.String()))
	}
	return bin, cleanup, nil
}

// capture runs the binary and returns stdout and stderr together.
//
// Both streams, and that is the whole trap. Every subcommand's flag help goes
// to STDERR — the flag package writes it there — while `help` and `commands`
// print to stdout. Capturing stdout alone yields an empty string for every
// command that has flags, and the page would build, publish and look plausible
// with nothing on it.
//
// A non-zero exit is an error. That is only honest because asking for help is
// no longer a failure: `-h` used to exit 1 on four of these commands.
func capture(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s exited non-zero: %w: %s",
			filepath.Base(bin), strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}
