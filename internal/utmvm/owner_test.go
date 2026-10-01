package utmvm

import (
	"errors"
	"strings"
	"testing"
)

// TestResolveCallerPrecedence: -owner beats IRGO_WINVM_OWNER beats the MCP
// client beats the person at the terminal, and only the last is the owner.
//
// Negative control, run by hand: swap the flag and env cases in ResolveCaller
// and the first row fails.
func TestResolveCallerPrecedence(t *testing.T) {
	cases := []struct {
		flag, env, client string
		wantID, wantSrc   string
		human             bool
	}{
		{"me", "env-agent", "claude-code", "me", SourceFlag, false},
		{"", "env-agent", "claude-code", "env-agent", SourceEnv, false},
		{"  ", "", "claude-code", "claude-code/" + defaultOwner(), SourceMCP, false},
		{"", "", "", defaultOwner(), SourceDefault, true},
	}
	for _, c := range cases {
		got := ResolveCaller(c.flag, c.env, c.client)
		if got.ID != c.wantID || got.Source != c.wantSrc || got.Human() != c.human {
			t.Errorf("ResolveCaller(%q, %q, %q) = %+v human=%v; want %s from %s human=%v",
				c.flag, c.env, c.client, got, got.Human(), c.wantID, c.wantSrc, c.human)
		}
	}
}

// TestTwoMCPClientsFromTwoReposDiffer: every Claude Code session is called
// "claude-code", so the client name alone would give agents from different
// repositories one identity. The repository the server runs in tells them
// apart.
//
// Negative control, run by hand: drop the "/" + defaultOwner() suffix in
// ResolveCaller and the two IDs are equal.
func TestTwoMCPClientsFromTwoReposDiffer(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t.Chdir(a)
	ca := ResolveCaller("", "", "claude-code")
	t.Chdir(b)
	cb := ResolveCaller("", "", "claude-code")
	if ca.ID == cb.ID {
		t.Fatalf("two MCP clients in two directories share the identity %q", ca.ID)
	}
	if !strings.HasPrefix(ca.ID, "claude-code/") || !strings.Contains(ca.ID, "@") {
		t.Fatalf("MCP identity %q is not <client>/user@host:repo", ca.ID)
	}
}

// TestCheckVMChoice: the owner's VM is the default only for the owner. Any
// other caller who leaves -vm out is refused, and naming it, or any other VM,
// is allowed.
//
// Negative control, run by hand: return nil when !given in CheckVMChoice and
// the agent-default row is allowed.
func TestCheckVMChoice(t *testing.T) {
	human := Caller{ID: "me@mac:repo", Source: SourceDefault}
	agent := Caller{ID: "claude-code/me@mac:other", Source: SourceMCP}
	named := Caller{ID: "ci", Source: SourceEnv}
	cases := []struct {
		name   string
		c      Caller
		vm     string
		given  bool
		refuse bool
	}{
		{"the owner, no -vm", human, DefaultVMName, false, false},
		{"an agent, no -vm", agent, DefaultVMName, false, true},
		{"IRGO_WINVM_OWNER set, no -vm", named, DefaultVMName, false, true},
		{"an agent naming the owner's VM", agent, DefaultVMName, true, false},
		{"an agent naming the owner's VM in capitals", agent, "IRGO-WIN11", true, false},
		{"an agent naming its own VM", agent, "z1", true, false},
	}
	for _, c := range cases {
		err := CheckVMChoice(c.c, c.vm, c.given)
		if got := errors.Is(err, ErrDefaultVMReserved); got != c.refuse {
			t.Errorf("%s: refused=%v (%v), want %v", c.name, got, err, c.refuse)
		}
		if c.refuse && err != nil && !strings.Contains(err.Error(), "vm-create -vm") {
			t.Errorf("%s: the refusal does not say how to get a VM of one's own: %v", c.name, err)
		}
	}
}

// TestOwnerKeyIsReadableAndUnique: a caller's staging directory is named so
// a person can tell whose it is, two identities that read alike never share
// one, and no identity can name a path outside bin/.
//
// Negative control, run by hand: drop the hash suffix in ownerKey and "a:b"
// and "a@b" collide.
func TestOwnerKeyIsReadableAndUnique(t *testing.T) {
	if got := ownerKey("ci-runner"); got != "ci-runner" {
		t.Errorf("a plain name became %q", got)
	}
	k := ownerKey("claude-code/apple@mac:irgo-windows-vm")
	if !strings.HasPrefix(k, "claude-code-apple-mac-irgo-windows-vm-") {
		t.Errorf("an MCP identity became %q, which a person cannot read", k)
	}
	if ownerKey("a:b") == ownerKey("a@b") {
		t.Error(`"a:b" and "a@b" share a key`)
	}
	for _, id := range []string{"../x", "..", "/", "", "a/../../b", strings.Repeat("x", 300)} {
		k := ownerKey(id)
		if k == "" || strings.ContainsAny(k, `/\`) || strings.HasPrefix(k, ".") || len(k) > 64 {
			t.Errorf("ownerKey(%q) = %q, not a safe directory name", id, k)
		}
	}
}
