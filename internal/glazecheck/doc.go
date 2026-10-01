// Package glazecheck answers "does glaze work?" and writes the answer down —
// and, with the same runner, "does this VM have what the project relies
// on?" (the VM suite, vm.go: examples/vmconformance, docs/VM-STATUS.md). What
// differs between the two is a Suite value; everything below describes both,
// in glaze's words.
//
// The question is examples/conformance, a go test suite. `glaze-check`
// compiles it with `go test -c`, runs the binary with -test.v=test2json — on
// this machine, or on the VM through app-create — converts what it printed
// with `go tool test2json`, and records every test's outcome and first message
// in docs/GLAZE-STATUS.md; `glaze-status` reads it back. Before this existed
// the mise tasks printed YES or NO and threw it away, so "does glaze work on
// Windows?" could only be answered by someone re-running a minute and a half
// of VM work, and an agent could not ask at all.
//
// It used to run four standalone programs and grep their output for FAIL,
// FAILED or ERROR. That could not tell a skipped capability from a missing
// one, lost everything after a program's first failure, and gave one result
// for a program that checked six things. Structured events from the testing
// package are the answer to all three.
//
// Known upstream failures (KnownUpstream) still fail and are still recorded
// as failures; they only change the verdict from NO to KNOWN BUGS ONLY, so a
// gate goes red for a new failure and not for one already recorded in docs/UPSTREAM.md.
//
// # Why it is in the shipped binary
//
// It needs the repository's source (examples/) and a Go toolchain, which a
// user who downloaded irgo-winvm to run their own .exe has neither of. It is
// here anyway, for three reasons:
//
//   - docs/DEVELOPMENT.md: logic belongs in the binary, not in mise.toml. The
//     check was two shell scripts in the task file, and recording a verdict
//     with versions and first-failure lines is not a job for shell.
//   - An MCP tool is a command — mcpserver generates its tools from
//     command.All and holds no behaviour of its own — so an agent can only ask
//     for a check, or for the recorded answer, if the binary has one.
//   - It is the question this repository exists to answer. `.mcp.json` starts
//     the server from a checkout, so the agent most likely to call it is
//     exactly the one that has the source.
//
// It does not link glaze or native. The examples are built by running `go` as
// a subprocess, so the libraries under test stay out of the tool's dependency
// graph (cmd/irgo-winvm/deps_test.go guards that). Outside a checkout, both
// commands fail at once and say where they looked — see ErrNoRepo.
//
// # Why not in utmvm
//
// utmvm is the three stages and what they touch. This is a consumer of the
// app stage, like the CLI is, and it depends on a source tree that utmvm must
// never need: iso-create works on a machine that has never seen this repo.
package glazecheck
