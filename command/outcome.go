package command

// What a run ended as, declared once.
//
// The CLI exits with these numbers and the MCP server reports them to an agent,
// and the two must agree about what each one means — including which one is
// worth retrying, which is the only piece of this that changes what a caller
// does next.
//
// The alternative was a `case 4: retryable = true` in the server beside the
// constants in package main: two statements of one contract, and the kind that
// disagrees quietly, because nothing ever compares them.

// Code is a process exit status, and the classification of a tool result.
type Code int

// The codes. utmctl exits 0 when it fails — documented in AGENTS.md — so this
// tool is the only honest signal a caller gets, and it had better say something
// specific.
const (
	CodeOK Code = 0

	// CodeFailed is the guest program's own failure, and the default for
	// anything not classified below.
	CodeFailed Code = 1

	// CodeUsage matches what the flag package uses for a malformed flag.
	CodeUsage Code = 2

	CodeNoVM      Code = 3
	CodeNoAgent   Code = 4
	CodeNeedForce Code = 5

	// CodeBusy is a refusal: another mutation holds the lock, so this one was
	// not started. Retryable — wait and ask again — but only by polling, since
	// the holder finishes on its own schedule.
	CodeBusy Code = 6
)

// Outcome describes a code to whoever has to act on it.
type Outcome struct {
	Code Code

	// Name is the machine-readable form, for a caller that should not be
	// parsing English. An agent matching on the wording of "that VM does not
	// exist" breaks the day the sentence is reworded.
	Name string

	// Meaning is the sentence a person reads.
	Meaning string

	// Retryable marks the outcomes worth trying again.
	//
	// Windows Update takes the guest agent away for minutes at a time; the VM
	// is fine and will answer. An agent that cannot tell this from "no such
	// VM" either gives up on a working VM or retries forever against one that
	// is not there. "Busy" is the same shape: the holder of the mutation lock
	// will finish on its own, and the caller should wait rather than conclude.
	// It is the single most useful bit in this file.
	Retryable bool
}

// Outcomes is every code, in order.
var Outcomes = []Outcome{
	{CodeOK, "ok", "it worked — including -h, and an undo that found nothing to undo", false},
	{CodeFailed, "failed", "your program ran and failed; its own exit code is named in the message, not passed through", false},
	{CodeUsage, "usage", "the command was called wrongly", false},
	{CodeNoVM, "no-vm", "that VM does not exist", false},
	{CodeNoAgent, "no-agent", "the VM is there, the guest agent is not answering — wait and try again", true},
	{CodeNeedForce, "need-force", "refused: a destructive command without -force", false},
	{CodeBusy, "busy", "another mutation is in progress — wait and try again", true},
}

// Classify returns the outcome for a code.
//
// An unknown code is reported as such rather than defaulted to failure: a
// caller told "failed" about a number nobody declared has been given a wrong
// answer, and "cannot tell" is not "safe".
func Classify(c Code) (Outcome, bool) {
	for _, o := range Outcomes {
		if o.Code == c {
			return o, true
		}
	}
	return Outcome{Code: c, Name: "unknown", Meaning: "an exit code this tool does not declare"}, false
}
