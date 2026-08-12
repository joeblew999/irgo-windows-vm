package utmvm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// EnsureUTM returns the installed UTM, installing it if missing.
//
// A developer should not have to read a prerequisites list before the tool
// works. Homebrew is used when present; otherwise UTM's signed .dmg is
// downloaded from its GitHub release and the app copied out, so nothing has to
// be installed first.
func EnsureUTM() (Install, error) {
	in, err := DetectUTM()
	if err == nil {
		return in, nil
	}
	if !errors.Is(err, ErrUTMNotInstalled) {
		return in, err
	}

	// Download it ourselves rather than requiring Homebrew.
	//
	// "Install Homebrew, then install UTM" is two prerequisites for a tool
	// whose whole claim is that a single binary sets the machine up. UTM
	// publishes a signed .dmg on its GitHub releases, so there is nothing to
	// require: fetch it, mount it, copy the app out.
	//
	// Homebrew is still used when it is present, because a developer who
	// manages their applications with it will want UTM in that inventory rather
	// than dropped into /Applications behind its back.
	if BrewPath() != "" {
		if runErr := BrewInstall("utm", true); runErr == nil {
			return DetectUTM()
		}
		fmt.Fprintln(os.Stderr, "Homebrew could not install it; downloading the .dmg instead...")
	}

	if dlErr := InstallUTMFromRelease(nil); dlErr != nil {
		return in, dlErr
	}
	return DetectUTM()
}

// GuestToolsURL is where UTM itself downloads the guest tools from.
//
// Taken from the string table of UTM.app's own binary rather than guessed, so
// it is the URL the application uses and not one that merely happens to work
// today.
//
// Fetching it ourselves is what makes a one-command setup possible at all. The
// alternative — and what this used to say — was to tell the developer to open
// UTM, create a throwaway VM and pick "Install Windows guest tools" from a
// menu. That is not a step a setup command can take, it is not discoverable,
// and skipping it produces a VM that boots perfectly and is then unreachable:
// no network, no `utmctl exec`, no IP, and nothing saying why.
const GuestToolsURL = "https://getutm.app/downloads/utm-guest-tools-latest.iso"

// EnsureGuestTools returns the guest tools ISO, downloading it if UTM has not
// already cached one.
//
// It writes to UTM's own cache location, so UTM and every VM generated
// afterwards pick it up exactly as if the GUI had fetched it.
func EnsureGuestTools() (string, error) {
	p, err := GuestToolsISO()
	if err == nil {
		return p, nil
	}
	return FetchGuestTools(nil)
}

// FetchGuestTools downloads the guest tools into UTM's cache and returns the
// path. progress, if non-nil, is called as bytes arrive.
func FetchGuestTools(progress func(done, total int64)) (string, error) {
	dest, err := guestToolsPath()
	if err != nil {
		return "", err
	}
	if _, sErr := os.Stat(dest); sErr == nil {
		return dest, nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
		return "", mkErr
	}

	// No SHA to check against: UTM publishes none, and the file it serves
	// changes with every guest-tools release. The size check below is the only
	// guard available — a truncated download here presents later as a VM with
	// no network, which is a long way from the cause.
	if dErr := Download(GuestToolsURL, dest, "", progress); dErr != nil {
		return "", fmt.Errorf("downloading UTM guest tools: %w\n"+
			"  Alternatively, open UTM once and choose \"Install Windows guest tools\" "+
			"from any VM's menu; it caches the ISO in the same place", dErr)
	}
	fi, sErr := os.Stat(dest)
	if sErr != nil {
		return "", sErr
	}
	if fi.Size() < 10<<20 {
		_ = os.Remove(dest)
		return "", fmt.Errorf("guest tools download was only %s, which is too small to be the ISO",
			HumanBytes(fi.Size()))
	}
	return dest, nil
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
