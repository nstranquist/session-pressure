# Browser admission: memory-derived slots (2026-11-17, WORK-1114 under WORK-1065)

## Problem

On 2026-11-17 three agent sessions on the M4 (48 GB) were blocked by browser
admission:

- `work run --class browser` took one host-wide file lock
  (`browser-execution`) before it queued for a work lease, and held it during
  the whole lease wait. A CSP sweep (`--wait 20m`) held it for more than 15
  minutes while it queued, so console screenshots and smoke runs in other
  sessions failed at once.
- `--wait D` did not apply to the browser gates. `BROWSER_PAUSED` and a busy
  lock refused immediately (exit 14).
- `ndev browser open` refused when any managed session was alive
  ("Browser session slot occupied"), a second fixed cap of one.

## Decision

No fixed browser cap. Browser concurrency is derived at each admission from
memory headroom and the measured per-session cost of the runtime:

    slots = active + floor((headroom - charged) / session cost)

- `headroom` is the shared memory-headroom view (`work_headroom.go`):
  available memory minus the reserve (physical / 8) minus in-flight lease
  predictions.
- `session cost` = max(runtime cost, the command's own measured peak).
  Runtime costs come from the snapref runtime-cost benchmark (nicos-tools
  `packages/snapref/cmd/snapref-runtime-cost`, receipt
  `benchmark/runtime-cost.json`, delivery 47c5778b): headless-shell 290 MB,
  Chrome for Testing 928 MB. Unknown runtime uses the CFT cost. ndev sets
  `SESSION_PRESSURE_BROWSER_RUNTIME` from snapref's predicted runtime.
- `charged` is the cost of browser admissions that the latest resident sample
  cannot include yet: commands still queued for a lease, and commands that
  started after the sample (minus 2 s skew). A detached `ndev browser open`
  session outlives its command, so the charge outlives it too until a newer
  sample covers the session. The ledger is `browser-admissions.json` under
  the `browser-admission` file lock. Rows are dropped after the 3-minute
  in-flight ramp.
- Without a fresh sample the safe answer is one browser at a time.
- CPU pressure stays with the browser control (`BROWSER_PAUSED`).

`work run --class browser --wait D` now waits through both `BROWSER_PAUSED`
and missing headroom until the deadline, with progress lines on stderr
(`blocker=browser_paused` / `blocker=browser_memory`). The browser wait spends
the same `--wait` budget as the work queue. `--wait 0` still decides once.
ndev browser passes `--wait 1m` by default (`NDEV_BROWSER_WAIT` overrides).

On the M4 at 23 GB headroom this is about 79 headless-shell or 24 CFT
sessions before memory refuses; the weighted work capacity (browser weight 3
of 12) and the browser control bound CPU first.

## Code

- `sessionpressure/browser_admission.go`: cost model, ledger, decision,
  `waitForBrowserAdmission`.
- `sessionpressure/workrun.go`: browser admission runs after the placement
  key is known and before the work queue; `Started()` at child start,
  `Release()` at exit. `AcquireBrowserSlot` is deleted.
- Tests: `sessionpressure/browser_admission_test.go`.

## Not done

- The runtime costs are fixed priors from one benchmark receipt. A later
  slice can learn them from sampled snapref session RSS.
