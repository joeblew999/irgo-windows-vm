# For agents

All documentation is in [docs/](docs/). This file only points there.

**Before writing code, read [Conventions](docs/CONVENTIONS.md),
[Architecture](docs/ARCHITECTURE.md) and [Known traps](docs/TRAPS.md)**, and
check what already exists before adding anything. Most of the duplication this
project has had to clean up was written by an agent that did not check.

| file | read it before you |
|---|---|
| [docs/CONVENTIONS.md](docs/CONVENTIONS.md) | write any code: the rules, and the defect behind each |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | change a package, a lock, a job, the data on disk, pushes, the golden image or the cache |
| [docs/TRAPS.md](docs/TRAPS.md) | touch UTM, the ISO, the answer file, the guest or a window on Windows |
| [docs/WORKER.md](docs/WORKER.md) | change `worker/` |
| [docs/TESTING.md](docs/TESTING.md) | change `examples/`, or claim glaze works |
| [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) | push, release, or add a doc section ([where it goes](docs/CONTRIBUTING.md#where-a-topic-goes)) |
| [docs/USING.md](docs/USING.md) | change what a command does for its user, or an exit code |
| [docs/UPSTREAM.md](docs/UPSTREAM.md) | work around anything in glaze, native or UTM (don't: fix it there) |
| [docs/RESULTS.md](docs/RESULTS.md) | state a number: what has been measured, dated |
| [docs/GLAZE-STATUS.md](docs/GLAZE-STATUS.md) | say whether glaze works (generated: never edit it) |
| [docs/VM-STATUS.md](docs/VM-STATUS.md) | say whether a VM has what the project relies on (generated: never edit it) |
| [docs/ROADMAP.md](docs/ROADMAP.md) | pick up what is next |
| [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md) | touch the HTTP transport, or open a port in the guest |

Using `irgo-winvm` from another repository, and filing an issue here:
[For agents](docs/FOR-AGENTS.md).
