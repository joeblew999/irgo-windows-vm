# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order

Nothing open. Done: [`done/`](done/) — upstream workflow restored (`upstream:*`), glaze#34 reported,
VM hardened (`vm-repair`, never-expiring password, fail-fast `-gui`).

## Working on glaze or native

```sh
mise run upstream:clone && mise run upstream:verify
mise run upstream:lint && mise run upstream:test:windows
mise run app:create:verify     # probes on the VM; if -gui fails: irgo-winvm vm-repair -reboot
mise run upstream:unlink
```
