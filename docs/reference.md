---
title: Reference
nav_order: 5
has_children: true
---

# Reference

Tables to look things up in. Three pages have no file here: the site generates
them from the code at build time, so they are only on
[the site](https://joeblew999.github.io/irgo-windows-vm/).

| Page | What it lists |
|---|---|
| [Commands](https://joeblew999.github.io/irgo-windows-vm/reference.html) | every command and flag, captured from the binary (`irgo-winvm help`, and `-h` for each) |
| [MCP](https://joeblew999.github.io/irgo-windows-vm/mcp.html) | the MCP server's tools, arguments and instructions, captured from a live server |
| [Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html) | every endpoint of the Worker, from its route table in `wire/` |
| [Known traps](reference/traps.md) | what fails silently or misleadingly, one line each |
| [Upstream bugs](reference/upstream.md) | the bugs found in glaze, native and UTM, and their status |
| [Glaze status](GLAZE-STATUS.md) | does glaze work on the Mac and on Windows: the last recorded run of each (generated) |
| [VM status](VM-STATUS.md) | does each Windows VM have what this project relies on: the last recorded check (generated) |

The exit codes are in [What it exits with](guides/using.md#what-it-exits-with),
and the tasks in [mise tasks](contributing.md#mise-tasks).
