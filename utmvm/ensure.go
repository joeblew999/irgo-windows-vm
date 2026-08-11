package utmvm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// AppPath is where Homebrew's cask and the direct download both install UTM.
const AppPath = "/Applications/UTM.app"

// VerifiedVersion is the UTM release this package's config schema was read
// from — not guessed, but taken from utmapp/UTM at that tag.
//
// This matters more than it looks. UTM's config.plist is decoded by Swift
// Codable with non-optional fields, and a schema mismatch surfaces as a single
// generic "cannot import this VM" with no indication of which field is wrong.
// Reading `main` instead of the matching tag is how an afternoon disappears:
// at the time of writing main was v5.0.4 while the installed app was v4.7.5,
// and they disagreed.
const VerifiedVersion = "4.7.5"

// SchemaConfigurationVersion is the value UTM v4 accepts. UTM rejects anything
// higher outright, so a major-version jump is a real compatibility signal
// rather than cosmetic drift.
const SchemaConfigurationVersion = 4

// ErrUTMNotInstalled is returned when UTM is absent and could not be installed.
var ErrUTMNotInstalled = errors.New("UTM is not installed")

// Install reports how UTM got onto the machine, for messages to the user.
type Install struct {
	Path    string
	Version string

	// Compatible is false when the installed major version differs from the one
	// the schema was verified against. Generation is still attempted — refusing
	// outright would be worse than trying and reporting a clear failure — but
	// the caller should say so up front, because that is the likely cause of
	// any "cannot import" that follows.
	Compatible bool
}

// DetectUTM reports the installed UTM, or ErrUTMNotInstalled.
func DetectUTM() (Install, error) {
	var in Install
	if _, err := os.Stat(AppPath); err != nil {
		return in, ErrUTMNotInstalled
	}
	in.Path = AppPath

	// plutil ships with every macOS install and this file only ever runs on
	// macOS, so reaching for it is not a dependency a developer must satisfy.
	// App Info.plist files are usually binary, so plain XML parsing would fail
	// on most machines — the alternative is a binary-plist decoder for one
	// string.
	out, err := exec.Command("plutil", "-extract", "CFBundleShortVersionString",
		"raw", "-o", "-", AppPath+"/Contents/Info.plist").Output()
	if err != nil {
		// Present but unreadable: still usable, just unknown.
		in.Version = "unknown"
		in.Compatible = true
		return in, nil
	}
	in.Version = strings.TrimSpace(string(out))
	in.Compatible = sameMajor(in.Version, VerifiedVersion)
	return in, nil
}

// EnsureUTM returns the installed UTM, installing it via Homebrew if missing.
//
// Follows the same shape as irgo's ensureOSPackage: a developer should not have
// to read a prerequisites list before the tool works. Homebrew is the only
// route attempted — UTM's other distribution is a notarised DMG that cannot be
// installed unattended.
func EnsureUTM() (Install, error) {
	in, err := DetectUTM()
	if err == nil {
		return in, nil
	}
	if !errors.Is(err, ErrUTMNotInstalled) {
		return in, err
	}

	brew, lookErr := exec.LookPath("brew")
	if lookErr != nil {
		return in, fmt.Errorf("%w, and Homebrew is not available to install it.\n"+
			"Install UTM from https://mac.getutm.app or `brew install --cask utm`", ErrUTMNotInstalled)
	}

	fmt.Fprintln(os.Stderr, "UTM not found — installing with Homebrew (this takes a minute)...")
	cmd := exec.Command(brew, "install", "--cask", "utm")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return in, fmt.Errorf("installing UTM: %w", runErr)
	}
	return DetectUTM()
}

// EnsureGuestTools returns the guest tools ISO path, explaining how to get it
// when absent.
//
// UTM downloads this itself on first use and exposes no supported way to fetch
// it, so this cannot be auto-installed the way the app can. Without it the VM
// still boots, but the QEMU guest agent is never installed and utmctl exec and
// ip-address stay unavailable — so the failure is worth naming clearly rather
// than letting the VM come up mysteriously undriveable.
func EnsureGuestTools() (string, error) {
	p, err := GuestToolsISO()
	if err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%w\n\nTo fetch them: open UTM, create any VM, and choose "+
		"\"Install Windows guest tools\" from the VM menu once. UTM caches the ISO "+
		"and every VM generated afterwards will pick it up automatically", err)
}

// sameMajor compares leading version components. A patch or minor difference
// from the verified version has never broken the schema in practice; a major
// difference is exactly when UTM has changed ConfigurationVersion before.
func sameMajor(got, want string) bool {
	g, w := majorOf(got), majorOf(want)
	return g != -1 && g == w
}

func majorOf(v string) int {
	part, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(part)
	if err != nil {
		return -1
	}
	return n
}
