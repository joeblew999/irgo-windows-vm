package utmvm

// Installing the things this project needs, in one place.
//
// There are three of them — UTM, wimlib, xorriso — and before this they were
// three different stories: UTM shelled out to brew inline, wimlib and xorriso
// printed a `brew install` line for the developer to copy, and each had its own
// idea of what to say when brew was missing. Same job, three implementations,
// three behaviours.
//
// A developer running one binary should not be handed a shopping list. If brew
// is there, use it; if it is not, say so once, in one voice.

import (
	"fmt"
	"os"
	"os/exec"
)

// BrewPath returns the Homebrew binary, or "" when it is not installed.
func BrewPath() string {
	p, err := exec.LookPath("brew")
	if err != nil {
		return ""
	}
	return p
}

// BrewInstall installs a formula or cask.
//
// Output goes to stderr as it happens rather than being captured: these take
// tens of seconds to minutes, and a silent command that long reads as a hang.
func BrewInstall(name string, cask bool) error {
	brew := BrewPath()
	if brew == "" {
		return fmt.Errorf("Homebrew is not installed")
	}
	args := []string{"install"}
	if cask {
		args = append(args, "--cask")
	}
	args = append(args, name)

	fmt.Fprintf(os.Stderr, "installing %s with Homebrew...\n", name)
	cmd := exec.Command(brew, args...) //nolint:gosec // name comes from this package's own tables
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// Ensure makes the tool available, installing it with Homebrew if it is not.
//
// The error when that is impossible names the tool, what it is for, and the one
// command that fixes it — because "executable file not found in $PATH" tells a
// developer nothing about which of three tools is missing or why this project
// wants it.
func (t *Tool) Ensure() error {
	if t.resolve() {
		return nil
	}
	if BrewPath() == "" {
		return fmt.Errorf("%s is needed to %s, and Homebrew is not installed to fetch it.\n"+
			"  Install Homebrew from https://brew.sh, then: %s",
			t.Name, t.Why, t.Install())
	}
	if err := BrewInstall(t.Formula, false); err != nil {
		return fmt.Errorf("installing %s: %w\n  Try it by hand: %s", t.Name, err, t.Install())
	}
	if !t.resolve() {
		return fmt.Errorf("%s installed but %s is still not on PATH", t.Formula, t.Name)
	}
	return nil
}
