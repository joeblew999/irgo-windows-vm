package mcpserver

import "github.com/joeblew999/irgo-windows-vm/internal/utmvm"

// Instructions is what the server tells a connecting agent, in the
// initialize response, about using the tools together: the order, the jobs,
// the retries. The tools describe themselves one at a time; this is the part
// no single tool's description can say. `irgo-winvm mcp -h` prints it too, and
// the site's MCP page captures that, so there is one copy.
const Instructions = `irgo-winvm runs a Go program on a real Windows 11 ARM64 VM on this Mac and returns what it printed and its exit code. Use it to find out whether a Windows build actually works, which cannot be told by reading the code on macOS.

Start with doctor: it reports what is set up and lists the next steps in order. Flags are typed properties of each tool; anything positional, such as a path or a job id, goes in args.

The steps, each safe to repeat (done work is reported, not redone):
1. iso-create -fetch: the Windows installer (4.2 GB from Microsoft). Not needed when a golden image exists.
2. vm-create: a VM. With a golden image it is a clone that answers in seconds; without one, -install installs Windows unattended (about 45 minutes), or pulls the golden image from the user's private cache when IRGO_GOLDEN_URL and IRGO_GOLDEN_TOKEN are set. This Mac is shared: the default VM is its owner's, so a call without -vm is refused (usage, 2). Make a VM of your own, vm-create -vm <name>, and pass -vm <name> to every tool. vm-create refuses with no-room (7) when another VM would leave the Mac too little memory or disk, or you already have as many VMs as your quota allows; capacity says how much room there is, who holds it and how many more VMs fit, and status lists the running VMs and their owners.
3. app-create with the path to a .exe as its argument: pushes the program into the VM, runs it, returns its output. Build it with GOOS=windows GOARCH=arm64 CGO_ENABLED=0. Pass -gui for anything that opens a window, and -vm for your VM.

Long work returns a job id at once instead of blocking: iso-create -fetch, vm-create -install, vm-golden-create, vm-golden-push, vm-golden-pull and glaze-check -windows. Call status with the id as its argument until it has finished. vm-screen returns the VM's screen as an image, the only way to tell a stuck boot from a working one.

A failure is a tool result, not a protocol error, with structured content {code, status, retryable}. Match on status or code, never on the wording. no-agent (4), busy (6) and no-room (7, once a VM has stopped) are retryable: wait and call again. need-force (5) means a destructive tool (iso-delete, vm-delete, app-delete, vm-golden-delete) refused; add -force only when the user asked for that deletion.

You are identified by your client's name and the repository this server runs in; pass -owner to name yourself. Your uploads and your VMs are recorded as yours. A VM you made is yours to remove with vm-delete -vm <name> -force when you are done; vm-reap removes clones idle for a day.

glaze-check and glaze-status work only inside a checkout of the irgo-windows-vm repository.

Guide: ` + utmvm.SiteURL + `mcp.html`
