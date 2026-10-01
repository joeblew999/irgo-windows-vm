package utmvm

// Who is calling. Several independent callers share one Mac: the owner at a
// terminal, agents working in this repository, and agents from other
// repositories reaching the tool through its CLI or its MCP server. Every VM
// the tool makes records which of them made it (vm_lease.go), each caller
// stages uploads in its own part of bin/ (app_upload.go), and the owner's VM is
// not the default for anybody else (CheckVMChoice).
//
// The identity is a label, not an authentication: anyone can claim any name
// with -owner. It exists so callers who mean well do not collide, and so a
// person can see whose VM is whose. docs/DEVELOPMENT.md, "Sharing one Mac".

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// OwnerEnv names the caller when no -owner flag does. An agent from another
// repository sets it once in its environment.
const OwnerEnv = "IRGO_WINVM_OWNER"

// Where a caller's identity came from, in order of precedence.
const (
	SourceFlag    = "-owner"
	SourceEnv     = OwnerEnv
	SourceMCP     = "MCP client"
	SourceDefault = "default"
)

// Caller is who is running a command.
type Caller struct {
	ID     string // what is recorded as a VM's owner and names a staging space
	Source string // one of the Source constants
}

// Human reports whether this is the machine's owner at a terminal: the CLI
// run with no identity given. Anything that names itself, and anything over
// MCP, is another caller, and does not get the owner's VM by default.
func (c Caller) Human() bool { return c.Source == SourceDefault }

func (c Caller) String() string { return fmt.Sprintf("%s (from %s)", c.ID, c.Source) }

// ResolveCaller decides who is calling: an explicit -owner flag, else
// IRGO_WINVM_OWNER, else the MCP client's name from its initialize request,
// else user@host:repo for a person at a terminal.
//
// The MCP client's name alone is not enough: every Claude Code session calls
// itself "claude-code", so two agents from two repositories would share one
// identity, one staging space and each other's VMs. The repository the server
// was started in is added, which over stdio is the agent's own.
func ResolveCaller(flagOwner, envOwner, mcpClient string) Caller {
	switch {
	case strings.TrimSpace(flagOwner) != "":
		return Caller{ID: strings.TrimSpace(flagOwner), Source: SourceFlag}
	case strings.TrimSpace(envOwner) != "":
		return Caller{ID: strings.TrimSpace(envOwner), Source: SourceEnv}
	case strings.TrimSpace(mcpClient) != "":
		return Caller{ID: strings.TrimSpace(mcpClient) + "/" + defaultOwner(), Source: SourceMCP}
	}
	return Caller{ID: defaultOwner(), Source: SourceDefault}
}

// CallerFromEnv is ResolveCaller with the environment read, for the CLI.
func CallerFromEnv(flagOwner, mcpClient string) Caller {
	return ResolveCaller(flagOwner, os.Getenv(OwnerEnv), mcpClient)
}

// defaultOwner is user@host:repo, the repository being the nearest directory
// up from the working directory that holds .git (a file in a worktree), or
// the working directory's own name outside one. Walked rather than asked of
// git: it is called on every command, and git may not be installed.
func defaultOwner() string {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	repo := "?"
	if wd, err := os.Getwd(); err == nil {
		repo = filepath.Base(wd)
		for d := wd; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				repo = filepath.Base(d)
				break
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return name + "@" + host + ":" + repo
}

// ownerKey is a caller's identity as a file name: readable, so a person
// looking in bin/ can tell whose directory is whose, and unique, so two
// identities that read alike ("a:b" and "a@b") never share one. A plain name
// is used as it is; anything else has each run of other characters turned
// into "-" and a short hash of the whole identity appended.
func ownerKey(owner string) string {
	n := strings.ToLower(strings.TrimSpace(owner))
	if plainKey.MatchString(n) {
		return n
	}
	var b strings.Builder
	dash := false
	for _, r := range n {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		switch {
		case ok:
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if len(slug) > 48 {
		slug = slug[:48]
	}
	sum := sha256.Sum256([]byte(n))
	return strings.TrimPrefix(slug+"-", "-") + hex.EncodeToString(sum[:4])
}

// ErrDefaultVMReserved is a caller other than the owner reaching for the
// owner's VM without naming it.
var ErrDefaultVMReserved = errors.New("the default VM is reserved for the machine's owner")

// CheckVMChoice refuses a caller other than the owner who did not pass -vm and
// so would land on DefaultVMName, the owner's VM.
//
// Everyone defaulting to one VM meant unrelated callers queued on its lock
// (exit 6) and left their binaries, windows and state in a guest somebody else
// was using. A clone of the golden image is about 23 seconds, so the refusal
// says how to get one. Naming the VM explicitly is allowed: that is a
// deliberate choice, not an accident of defaults.
func CheckVMChoice(c Caller, vm string, given bool) error {
	if given || c.Human() || !strings.EqualFold(vm, DefaultVMName) {
		return nil
	}
	return fmt.Errorf("%w: you are %s, and %s is the owner's.\n"+
		"  Make a VM of your own, about 23 s from the golden image:\n"+
		"      irgo-winvm vm-create -vm <name>\n"+
		"  then pass -vm <name> to every command. To use %s anyway, pass -vm %s",
		ErrDefaultVMReserved, c, DefaultVMName, DefaultVMName, DefaultVMName)
}
