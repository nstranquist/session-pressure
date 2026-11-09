# Repository boundary incident review

Owner: Codex session `01a1bbc7-3cf2-72b0-b843-563bca14e3dd`.
Reviewed November 9, 2026 UTC. This review supersedes earlier statements that
made factory delivery completion a prerequisite for standalone product work.

## Cause and prevention

I combined the product audit and factory integration under WORK-41, applied
the factory delivery closeout to both, and treated my pending factory changes
as product blockers without tracing the actual app build/install dependency.
Prior audit notes recommended a coordinated factory promotion, but I failed
to check that advice against the extracted product's current authority.
This was a scope and dependency reasoning error.

The correction is explicit in the [repository boundary guide](../../repository-boundaries.md)
and linked from the product README. Each blocker must identify its repository,
the exact dependency, and the installed artifact it prevents. Factory queue
state cannot gate direct product app installation or live testing. The earlier
scope correction is committed as `1af04be`.

## Factory code audit

I inspected every delivery manifest for this session and the actual source
diffs: nine records, including an older superseded build-tool capture and the
new signing correction. The changes introduce no duplicate product engine,
policy, UI, or privileged trace implementation into nicos-tools. The legacy
desktop source predates this work and remains preserved.

| Captured work | Source/delivery identity | Assessment |
|---|---|---|
| Canonical CLI routing and product telemetry source registration | Landed `483b1de` | Factory adapter and integration; correct location |
| Generic guarded commit staging isolation | Landed `598a33c` | Shared commit tooling; correct location |
| Legacy app Makefile forwarding | Landed `ae8f228` | Correct adapter location, but forced ad-hoc signing; defect below |
| Failure operation/surface query filters | Delivery `b20237d3-8823-4e62-a034-9dd63b815231`, source `e059fda` | Generic factory telemetry; correct location |
| Verified product release promotion adapter | Delivery `196ac71c-82d8-4e09-83b8-0f890c054fbe`, source `2f6141e` | Delegates to product bundle guard; correct location |
| Native export/source closure receipts | Delivery `9de03888-f6d9-452d-ba3f-47903e2b7166`, source `2b738e6` | Generic committed build tooling; correct location |
| Earlier export versions | Held `2aca8f87-72ad-4d63-95cb-58f5a7248b90`; superseded `64c635e4-3fae-4254-bcd6-24e913bbaba0` | Later capture corrects target/host source-digest execution; not separate product behavior |
| Signing-default correction | Delivery `9b7e59ff-522c-44b7-8086-4d501008d696` | Removes factory override; both forwarding checks pass |

The real defect was in `apps/desktop/ndev-pressure/Makefile`: it retained
`CODE_SIGN_IDENTITY ?= -` and passed that value into the canonical product.
This overrode the product's Developer ID selection. The correction removes
both the default and explicit forwarding; recursive make still inherits an
explicit operator command-line override. A stub product target verified the
canonical default and explicit override paths. Private proof:
`review-factory-wrapper-signing.json`.

At capture, the correction was accepted with all capture gates passing in
13.090 seconds. Acceptance does not establish landing or installation. No
factory queue waiter was started for product progress. The native product
was installed directly with the real Developer ID authority.

## Live trace review

App 1.4.8 build 16 passed 89 Swift tests and strict matching Team ID checks.
The real trace first found a Service Management unregister/register timing
failure, then reached the privileged helper and failed administrator
authorization validation. It did not complete a trace.

Source `a1311f1` retains the originating AuthorizationRef until the asynchronous
trace finishes instead of freeing it before the helper imports its external
form. Failed and cancelled requests still release their reference. It also
yields one main-run-loop turn after unregister completion, following
[Apple DTS guidance](https://developer.apple.com/forums/thread/783539).
Eleven targeted tests pass in `review-trace-authorization-lifetime-tests.log`.
Installed live success must be recorded separately after promotion; source
tests do not prove it. Existing helper trust and authorization checks remain.

The 01:03 app-1.4.8 observer was used during interactive trace diagnosis.
Its elapsed time is diagnostic and cannot establish an idle resource pass.
