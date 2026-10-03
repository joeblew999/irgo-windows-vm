// Package vmconformance is the test suite that answers "does this VM have
// everything this project relies on?" — run inside the Windows guest, one
// named test per property, each with a failure message that says what is
// wrong and what puts it right.
//
// Everything the project fixed on the VM by hand or in the answer file is
// asserted here: the dev account and its never-expiring password, AutoLogon
// and a desktop session, Windows Update kept from restarting or prompting,
// both Windows keys remapped (and whether this boot has the remap), OneDrive
// and toasts off, Device Encryption off, hibernation off, the irgo-drop SMB
// share with its firewall rule open to the local subnet only and Windows'
// own "File and Printer Sharing (Restrictive)" rules off, WebView2 registered
// and present, sleep and the display timeout off, the answer file finished,
// enough free disk, OpenSSH Server installed and off until vm-ssh-create
// turns it on, and the Windows build.
//
// The checks only read. None changes the VM, so the suite can be pointed at
// irgo-win11, the shared VM, as safely as at a disposable clone.
//
// It runs twice, because Windows splits what can be seen:
//
//   - As SYSTEM, through the guest agent (app-create without -gui), every
//     test not named TestSession*: BitLocker's status, the SMB share and the
//     firewall need an administrator's token, which dev's session does not
//     have.
//   - In dev's desktop session (app-create -gui), the TestSession* tests:
//     whether there is a desktop, dev's own policies, a WebView2 window
//     rendering, and a picture of each setting as Windows itself reports it.
//
// `irgo-winvm vm-check` builds it with `go test -c` for windows/arm64, runs
// both parts with -test.v=test2json, and internal/glazecheck records every
// test in docs/VM-STATUS.md. A test run in the wrong place skips and says
// where it belongs, so running the binary by hand misleads nobody.
//
// Pictures go where the glaze suite's go (examples/shots): with
// -vmconformance.shots=<dir>, each TestSession test with something to see
// writes vm/<Test>.png and logs the line glazecheck reads, and closes every
// window it opened. What a check read is logged as "evidence: ..." and
// recorded beside its result; facts about the VM ("fact: windows=...") go at
// the top of its section.
//
// Every test file is built for windows only; elsewhere the package has the
// parsing helpers and their tests, and nothing that needs a guest.
package vmconformance
