# For agents

Everything about this repo is in [docs/](docs/README.md), the same pages people
read. Nothing is kept here, so there is one source of truth. The same index for
machines: https://joeblew999.github.io/irgo-windows-vm/llms.txt

Read, in this order:

1. [docs/README.md](docs/README.md): what the tool is for, what is what, and
   the index of every page.
2. [docs/rules.md](docs/rules.md): how code here is written, and the defect
   behind each rule. They are binding.
3. [docs/concepts/architecture.md](docs/concepts/architecture.md) and
   [docs/reference/traps.md](docs/reference/traps.md), before changing code.
   Check what already exists before adding anything: most of the duplication
   this project has had to clean up was written by an agent that did not check.
4. The page for the part you are changing, from the index.
5. [docs/writing.md](docs/writing.md) before you write or change a page in
   `docs/`, and [docs/contributing.md](docs/contributing.md) before you push or
   release.

When you learn or change something, write it in the page in `docs/` it belongs
to, and run `mise run docs:check` and `mise run site:check`. Don't add README
files elsewhere.

Using `irgo-winvm` from another repository, and filing an issue here:
[For agents](docs/guides/agents.md).
