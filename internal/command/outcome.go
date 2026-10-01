package command

// Code is a process exit status, and the classification of an MCP tool result.
// The CLI and the MCP server share these so they agree on what each means,
// including which are worth retrying.
type Code int

// The exit codes. utmctl exits 0 when it fails, so these are the only reliable
// signal a caller gets; docs/DEVELOPMENT.md documents them for people.
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

	// CodeBusy means another mutation holds the lock, so this one was not
	// started. Retryable once the holder finishes.
	CodeBusy Code = 6

	// CodeNoRoom means vm-create did not start another VM because it would
	// leave the Mac too little memory or disk, or because it could not find
	// out. Retryable once a VM stops.
	CodeNoRoom Code = 7

	// CodeNotRun is a remote job that never ran or never finished: cancelled,
	// expired in the queue with no Mac to take it, or its Mac stopped
	// reporting. Retryable: submit it again.
	CodeNotRun Code = 8
)

// Outcome describes a code to whoever has to act on it.
type Outcome struct {
	Code Code

	// Name is the machine-readable form, so callers need not match on Meaning,
	// which is prose and gets reworded.
	Name string

	// Meaning is the sentence a person reads.
	Meaning string

	// Retryable marks outcomes that can change by waiting: Windows Update takes
	// the guest agent away for minutes, and the lock holder finishes on its
	// own. A caller that cannot tell these from "no such VM" either gives up
	// on a working VM or retries forever against a missing one.
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
	{CodeNoRoom, "no-room", "refused: another VM would leave this Mac too little memory or disk, or that could not be determined — try again once a VM stops", true},
	{CodeNotRun, "not-run", "a remote job did not run to the end — cancelled, no Mac took it in time, or its Mac went away; submit it again", true},
}

// Classify returns the outcome for a code. An undeclared code is reported as
// unknown, with ok false, rather than defaulted to failure.
func Classify(c Code) (Outcome, bool) {
	for _, o := range Outcomes {
		if o.Code == c {
			return o, true
		}
	}
	return Outcome{Code: c, Name: "unknown", Meaning: "an exit code this tool does not declare"}, false
}
