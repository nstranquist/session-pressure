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
diffs: eleven records, including an older superseded build-tool capture, the
signing correction, and two documentation captures. The changes introduce no duplicate product engine,
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
| Canonical product ownership and portable documentation links | Deliveries `8ce25ca2-ac12-41d4-8e28-df19c69040fb`, `d3c018ae-e8b1-4e10-88f4-34b736ce110a` | Legacy README points to the standalone authority; no new product behavior |

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

The 01:31 UTC refresh still reports the signing correction and documentation
captures as accepted. Their remaining factory delivery work does not prevent
product source verification, direct app installation, or the live trace.

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
Existing helper trust and authorization checks remain.

Installed app 1.4.9 build 17 completed the real 15-second trace on retry. The
app displayed bounded, truncated path output and remained alive. Only the
categorical outcome was retained; raw paths were not copied into the receipt.
The app and helper passed strict Apple signature and matching Team ID checks.

Source `074f89d` adds a bounded retry only for the observed Service Management
code-1 transition after our own successful unregister. Ordinary registration,
unrelated errors, cancellation, and required approval do not retry. Sixteen
targeted tests pass. Installed app 1.4.10 build 18 passes 94 full tests and
strict app/helper signing checks. Its first stable-process request passed
registration and reached authorization without a manual registration retry.

That request exposed a UI defect: synchronous `AuthorizationCopyRights` blocked
the main thread while awaiting credentials. The process sample records the
exact call stack in `review-app1410-hang.sample.txt`. Source `97d703f` uses
[Apple's asynchronous authorization API](https://developer.apple.com/documentation/security/authorizationcopyrightsasync(_:_:_:_:_:)).
The originating reference stays owned by the suspended caller; denial and
cancellation release it, cancellation after approval stops before XPC, and
the callback frees its separately returned rights set. Callback construction
is outside MainActor isolation. Sixteen targeted tests pass again.

Installed app 1.4.11 build 19 includes `97d703f`, passes all 94 tests, and passes
independent strict Apple signature checks for both app and helper with Team ID
`C82D23RNQ9` (`review-app1411-signing.json`). The first stable-process trace
request reached administrator authorization, and the app continued to answer
accessibility requests and refresh live samples while authorization was pending.
The final request awaits human credential entry in the system prompt; the
computer-use tool refuses access to SecurityAgent. No completed 1.4.11 trace
or idle observation is claimed. This is an explicit human-input boundary,
not a delivery queue dependency. The earlier completed 1.4.9 trace remains
separate evidence for the retained-authorization correction.

The 01:03 app-1.4.8 observer was used during interactive trace diagnosis.
Its elapsed time is diagnostic and cannot establish an idle resource pass.
