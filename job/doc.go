// Package job is where work that outlives the thing that started it will live.
//
// Nothing is implemented here yet. The package exists so the decision is made
// once, in the open, rather than by whichever file happens to need it first.
//
// # What it is for
//
// `vm-create -install` takes about 45 minutes. Over MCP every client times out
// long before that, and a call that blocks is abandoned while the install
// carries on — leaving an agent that cannot tell running from finished from
// dead. So the long commands will start the work, return an ID, and answer
// questions about it later. That ID, the process behind it, its stage and its
// elapsed time are what this package will own.
//
// # Why it is not in utmvm
//
// It is not iso, not vm, and not app. All three can start long work and the MCP
// server asks about all three, so putting it in any one of them makes the other
// two reach across a boundary AGENTS.md draws deliberately. The rule against
// splitting utmvm protects three stages that are genuinely coupled; it is not a
// reason to move an unrelated concern in beside them.
//
// # The one thing it must be able to do
//
// Report that a process is dead.
//
// A handle that answers "still running" forever because nothing checks is worse
// than no handle at all: an agent waits on it, the install is gone, and nothing
// says so. That is the failure AGENTS.md names — nothing returns success
// without checking it did the thing — and it decides the design. Whoever owns
// this owns process liveness, which is why it is a package and not a struct
// bolted onto a stage.
//
// # Where it writes
//
// Under the existing ~/Library/Application Support/irgo-winvm/, in jobs/. No
// new top-level path: there is one place things go, and a second one is a
// second answer to where anything is.
//
// # What keys it
//
// An ID carried in the tool's arguments, never a connection or a session. The
// protocol has been stateless since revision 2026-07-28 and there is no session
// to hang state on; work must survive a disconnect, and a second start against
// running work must return the existing handle rather than launch a second
// install.
package job
