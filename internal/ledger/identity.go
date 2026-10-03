package ledger

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// identity is who and where, worked out once per Client.
type identity struct {
	dir   string
	host  string // the friendly name: this Mac's hostname without .local
	owner string // IRGO_WINVM_OWNER, else the login name
	repo  string // IRGO_WINVM_REPO, else the git checkout the tool runs in
	once  sync.Once
	id    string
}

func newIdentity(dir string) *identity {
	h, _ := os.Hostname()
	owner := os.Getenv("IRGO_WINVM_OWNER")
	if owner == "" {
		owner = os.Getenv("USER")
	}
	if owner == "" {
		owner = os.Getenv("USERNAME")
	}
	repo := os.Getenv("IRGO_WINVM_REPO")
	if repo == "" {
		if wd, err := os.Getwd(); err == nil {
			repo = repoOf(wd)
		}
	}
	return &identity{
		dir:   dir,
		host:  strings.ToLower(strings.TrimSuffix(h, ".local")),
		owner: owner,
		repo:  repo,
	}
}

// machine is this machine's id: 16 random hex digits made the first time and
// kept in dir/machine-id. Random, not a hash of a hardware serial, so it
// identifies nothing outside the ledger; the hostname beside it is what a
// person reads. A runtime directory that is wiped starts a new id.
func (id *identity) machine() string {
	id.once.Do(func() {
		p := filepath.Join(id.dir, "machine-id")
		if b, err := os.ReadFile(p); err == nil && isMachineID(strings.TrimSpace(string(b))) {
			id.id = strings.TrimSpace(string(b))
			return
		}
		fresh := NewID()[:16]
		if err := os.MkdirAll(id.dir, 0o700); err == nil {
			// O_EXCL: two processes making it at once agree on the first.
			if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				_, wErr := f.WriteString(fresh + "\n")
				if cErr := f.Close(); wErr == nil && cErr == nil {
					id.id = fresh
					return
				}
			} else if b, err := os.ReadFile(p); err == nil && isMachineID(strings.TrimSpace(string(b))) {
				id.id = strings.TrimSpace(string(b))
				return
			}
		}
		id.id = fresh // unwritable: an id for this process, still not identifying
	})
	return id.id
}

// MachineID is this machine's id as the ledger knows it, kept in
// dir/machine-id (dir is the ledger's directory, <runtime>/ledger). The device
// report to fleet-api uses the same id, so a machine is one machine in both.
func MachineID(dir string) string { return newIdentity(dir).machine() }

func isMachineID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

var remoteURL = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)`)

// repoOf names the git checkout containing dir: owner/name from its first
// remote, else the checkout directory's name. A linked worktree (.git is a
// file) is named after the repository it belongs to, not the worktree. Read
// from .git directly: running git would cost a process per command.
func repoOf(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		gitPath := filepath.Join(d, ".git")
		st, err := os.Stat(gitPath)
		if err == nil {
			common, top := gitPath, d
			if !st.IsDir() {
				// "gitdir: <repo>/.git/worktrees/<name>"
				b, _ := os.ReadFile(gitPath)
				g, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
				if !ok {
					return filepath.Base(d)
				}
				common = filepath.Dir(filepath.Dir(g))
				top = filepath.Dir(common)
			}
			if name := remoteName(filepath.Join(common, "config")); name != "" {
				return name
			}
			return filepath.Base(top)
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// remoteName is owner/name from the first url in a git config, such as
// git@github.com:joeblew999/irgo-windows-vm.git or https://host/a/b.git.
func remoteName(config string) string {
	f, err := os.Open(config)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
	}
	m := remoteURL.FindStringSubmatch(b.String())
	if m == nil {
		return ""
	}
	u := strings.TrimSuffix(strings.TrimRight(m[1], "/"), ".git")
	u = strings.ReplaceAll(u, ":", "/")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}
