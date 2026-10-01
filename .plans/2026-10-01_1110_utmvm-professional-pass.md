# internal/utmvm professional pass

Status: queued (after Z and BB land — they edit utmvm)

## Symptom
The largest package still has long functions (VMCreate, RunInstall), mixed error prefixes
(44 of 148 `utmvm:`), war-story comments, an unused `EnsureReady` argument, ISO listing logic living
in cmd/, `ISOTool` mixed receivers.

## Change
Split long functions into named stages; one error convention; comments to Go norms (facts kept);
ISO listing/removal into utmvm; drop unused args. No behaviour change.

## Verify
go:check/go:lint; gocyclo/gocognit before/after; vm:test and app:test on a disposable clone.
