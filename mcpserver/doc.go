// Package mcpserver exposes irgo-winvm's commands over the Model Context
// Protocol, so an agent can drive a real Windows guest instead of guessing.
//
// That is the whole reason it exists. An agent writing a Go desktop app on a
// Mac cannot find out whether it works on Windows — a different windowing
// system, a different webview, and a set of failures that are invisible from
// here. This repository can answer that. The server is how the agent asks, and
// how it sees the screen when the answer is "it hung".
//
// # It holds no behaviour of its own
//
// Every tool calls the same utmvm functions the CLI calls, and registers from
// the same list in package command that the CLI dispatches on. Nothing here
// decides anything the CLI would decide differently.
//
// This is not tidiness. Behaviour that exists only when driven over MCP is a
// second answer to a question already answered, and it is the one nobody tests:
// the cycle tests drive the CLI, a developer drives the CLI, and a path
// reachable only from a protocol handler is exercised by neither. If a tool
// here needs logic, that logic belongs in utmvm where both callers get it.
//
// # Why it is not inside utmvm
//
// AGENTS.md says do not split utmvm, because iso, vm and app are coupled and
// separating them means one reaching into another's paths. That rule protects
// those three stages. It is not an argument for putting a protocol server in
// with them: the server depends on utmvm and on package command, and neither
// depends on it. Dependencies run one way, as they already do for vm and app
// over the ISO API.
package mcpserver
