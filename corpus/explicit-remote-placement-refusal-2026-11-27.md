# Explicit `--placement remote` refusals name the actual cause (2026-11-27)

## Problem

An audit of `work-control-events.jsonl` found 91 historical
`remote_work_declined` rows with the single reason:

```
explicit remote placement requires enabled remote Go routing; local fallback is disabled
```

Most occurred in one burst on 2026-11-17 (89 rows, 80 in one hour), with 2
later occurrences on 2026-11-20 and 2026-11-21 and zero since 2026-11-22. The
events adjacent to the 2026-11-20 and 2026-11-21 rows showed active remote Go
completions and `remote_enabled: true`, proving that remote Go routing was
already enabled when those refusals fired.

Inspecting `workrun.go` and `remote_node.go` exposed four defects on the
`--placement remote` fallthrough path:

1. **Wrong cause when routing is enabled.** Every `TryRemoteWork` return of
   `handled = false` (non-darwin host, disabled routing, disabled Node routing,
   ineligible Node check, or non-Go/non-Node command) fell through to one
   hardcoded string blaming disabled remote Go routing.
2. **Duplicate suffix and unrelated advice.** The hardcoded reason included
   `; local fallback is disabled`, which `refuseRemoteGo` appended a second
   time after the unrelated compiler-profile advice.
3. **Swallowed Node decline reason and duplicate event.** When Node routing was
   enabled and `planRemoteNodeWork` declined an ineligible check,
   `tryRemoteNodeWork` logged one `remote_work_declined` event and returned
   `handled = false`, so `workrun.go` discarded the Node reason and logged a
   second contradictory Go-routing refusal.
4. **Opaque telemetry.** Control events logged only `operation: "work"`, with
   no attribute distinguishing why routing declined or which binary was passed.

## Decision

Keep `--placement remote` strictly fail-closed (exit `workExitPolicyDenied`,
`WorkRefusalMarker` with `kind=remote_profile`, zero fallback to local
admission), and report the exact cause with actionable remediation and
structured telemetry attributes.

## Change

- `sessionpressure/workrun.go` and `sessionpressure/remote_work.go`: replaced
  the hardcoded fallthrough with `rejectExplicitRemotePlacement`, which
  separates `unsupported_host`, `routing_disabled`, `node_routing_disabled`,
  and `unsupported_command`, passes targeted remediation advice to
  `refuseRemoteGo` (eliminating the duplicate `local fallback is disabled` and
  compiler-profile text), and records `placement_decline_kind` and
  `command_bin` on the `remote_work_declined` control event.
- `sessionpressure/remote_node.go`: when `options.Placement ==
  WorkPlacementRemote` and `planRemoteNodeWork` declines a check,
  `tryRemoteNodeWork` now refuses directly with that exact planner reason and
  `placement_decline_kind: "node_ineligible"` in a single event. Auto placement
  is unchanged.

## Verification

- `TestRequiredRemotePlacementNeverEntersLocalAdmission` in
  `sessionpressure/work_placement_test.go` verifies all four causes (disabled
  routing, unsupported command, Node routing disabled, and ineligible Node
  check), asserts fail-closed refusal without entering local admission, checks
  for exactly one `local fallback is disabled`, verifies no compiler-profile
  advice, and checks the single structured `remote_work_declined` control
  event.
- `go test -count=1 -run 'TestRequiredRemotePlacement|TestParseWorkRunPlacement|TestLocalPlacement|TestRemoteNode' ./sessionpressure`
  and `go vet ./...` pass.
