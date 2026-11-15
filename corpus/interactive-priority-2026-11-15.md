# Interactive priority and the interactive red lane (2026-11-15)

Owner: claude/140b6102-38d5-49df-b599-984e22e11b5a.
Status: 2026-11-15, committed (`71b826d`, `31ab374`), installed in release `5fe6609`, live-verified.

Nico: "this queue is wasting everyone's time. is there anyway we can have
overrides for our agent chats?" He chose "light work under red", then
"interactive priority".

## Evidence

Last 1000 work events (2026-11-15 12:38Z to 14:55Z), express classes:

- 76 finished: 50 completed, 10 `wait_timeout`, 10 `wrapper_interrupt`,
  5 child failures, 1 dead owner.
- Wait: median 47.6 s, p90 450 s. Runtime: median 99 s, p90 678 s.
- Command peak RSS: median 163 MB, p90 660 MB, max 1274 MB (a `cmd/ndev-go`
  package test that compiled for 24 minutes under swap thrash).
- 22 queued and 8 cancelled events carried `pressure_dimension=memory`.

At the same time the host had 12 to 14 GB swap on 16 GB, 130 to 240 MB/s
swap-out, and level red. Every operation, from any chat, waited for the
whole episode, because a non-CPU red dimension returned "not allowed" with
no exception (`observe` in `workrun.go`). The fast lane covers CPU only.

## What changed

### Interactive marking

`work run` marks an operation interactive when:

1. `--interactive` or `--no-interactive` is given (the flag wins), else
2. `NDEV_INTERACTIVE=1` or `0`, else
3. `CLAUDE_CODE_SESSION_ATTENDED=1`. Claude Code sets this for processes
   spawned by a session a person is attending (`setSpawnedByAttendedSession`
   in the CLI). Headless runs do not set it.

A waiter started by a schedule (`NDEV_SCHEDULE_ID`) is never interactive.
Work events carry `interactive: true`, so the population is measurable.

### Interactive priority (selector)

`preferInteractiveWaiter` (`workoverride.go`) runs after operator pins and
after the ordinary policy (FIFO or bounded lookahead). It selects the oldest
interactive waiter that fits the remaining weighted capacity, with reason
`interactive_priority`. Bounds:

- A protected head keeps its drain (pressure reservation, exclusive
  capacity, bypass limit, bypass age).
- No overcommit, no green ride-through, no beyond-capacity override.
- Parked pressure reservations are skipped.
- Passing the head charges it one bypass. The existing rules then protect it
  after four bypasses or 30 s, so background work cannot starve.

Rejected: auto-applying the agent `--priority` pin. A pinned head is
admitted past the weighted cap when `allow_override_beyond_capacity` is on,
and that setting is on for this host. The pin does not record its actor, so
many attended chats (Nico runs many) would stack past capacity one head at
a time. Score 2/10. It would become viable if the pin stored its actor and
the beyond-capacity path applied only to operator pins.

### Interactive red lane (admission)

`workredlight.go`. Under red memory or swap pressure, an interactive
operation is admitted only when all of these hold:

- level is red, never critical; dimension is memory or swap, never storage;
- class weight is within `fast_lane_max_weight` (the express classes);
- the class's p90 command peak RSS over 24 h is known (at least 5 completed
  samples) and within `interactive_red_light_max_peak_rss_mb` (default 1024);
- coordinator status is readable, and all leased work including this
  operation stays within `interactive_red_light_capacity` (default 2 units).

After the lease is granted the gate checks again with the own lease counted
once, so three concurrent arrivals settle back to the cap. Telemetry label:
`interactive_red_light_admitted` or `red_light_refused:<reason>`. Setting
`interactive_red_light_capacity` to a negative value disables the lane.

## Proof

- runner-3, ad-hoc copy of the working tree (`/tmp/q3d-sp`), not the
  canonical wrapper, because session-pressure has no base clone there:
  `go vet ./sessionpressure/` clean; `go test ./... -count=1` all packages ok.
- New tests: `TestInteractiveRedLane*` (4), `TestPeakRSSP90NeedsEnoughCompletedSamples`,
  `TestInteractiveFromEnv`, `TestWorkRunInteractiveComesFromAttendedSession`,
  `TestSelectorPrefersInteractiveWorkThatFits`,
  `TestSelectorInteractivePreferenceKeepsProtectionAndCapacity`,
  `TestCoordinatorAdmitsInteractiveWaiterFirst`.
- Not covered: darwin-only code paths (the runner is Linux), and installed
  behavior on this Mac.

## Installed acceptance (2026-11-15 15:23Z)

- Release `5fe6609` (contains `71b826d`, `31ab374` and the light-command
  classification `5fe6609`). Bundle cloned on runner-3 under
  `/srv/build/exports/sp-5fe6609`, clean HEAD checked, `go vet ./...` and
  `go test ./... -count=1` passed there, darwin/arm64 built with Go 1.27.4
  and `pressure-darwin-clang`.
- CLI: `session-pressure-release install --expected-current 5f02c87…`;
  `current` reads `5fe6609`. Helper: `session-pressure monitor install`
  (enforcement comes from the persisted policy); `artifact status` reports
  `5fe6609`, drift false.
- Live, from an attended Claude Code chat, host at red memory (swap about
  9 GB): `ndev session pressure work run --class express-test --wait 3m --
  go version` queued with `pressure_dimension=memory` and was admitted in
  79 ms; all four events carry `interactive: true` and
  `admission_decision=interactive_red_light_admitted`.
- Live, same moment: `NDEV_INTERACTIVE=0 … --class build --wait 0 -- true`
  was refused with `ndev-work-refused/v1 kind=admission: NOT RUN: …
  heavy work blocked at red/memory`. Background heavy work stays held.

## Open items

- [x] Install, then confirm `interactive: true` and an
      `interactive_red_light_admitted` event in live history during red.
- [ ] Review after one day of events: red-lane admissions, any swap-out
      increase while they ran, and background wait change.
- [ ] Codex and Grok attended signals (only Claude Code's is known today;
      they can set `NDEV_INTERACTIVE=1`).
- [ ] App: show the interactive marker on Work Queue rows.
