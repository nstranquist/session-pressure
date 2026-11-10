# Work child wrapper boundary

Status: 2026-11-10 — host-bound regression passed; guarded landing and publication pending.
Owner: codex / 01a1c45e-6030-73f0-a304-9ef09415be44
Board decision: d-06gsb3bec7nq8irl9c

The tools delivery batch e2dd2215-2318-4d23-ae91-dd312458268f failed
catalog generation in run f4546043-c576-4260-8fac-f13243e5cf55. Admission
started after 547 ms. The generator then failed after 220654 ms with a
compatibility-wrapper recursion refusal while rebuilding repo-classes.

The outer ndev wrapper uses NDEV_PRESSURE_WRAPPER to detect forwarding
loops. The product forwarded this marker into the admitted generator, which
later invoked its candidate ndev for a provider build. That new invocation
was refused before it could resolve the product CLI.

Remove only this forwarding marker at the actual normal and inline nested
work child boundaries. Keep SESSION_PRESSURE_WORK_OPERATION, the live lease
and process-group checks, CPU limits, pinned product and candidate paths,
and host binding. Do not change the ndev wrapper's recursion refusal.

Acceptance requires a failing baseline and passing repaired process chain on
runner-3, related lease and environment regression checks, guarded source
landing, then serial product/helper and preflight publication with owner 458.
Preserve e0df2fd870ed13a7eb168f8d3e0f79db456a2752 ancestry and the CPU cap.
Verify the actual worker's product identity, then require catalog generation,
hook completion, and landing in one real tools attempt. The detached catalog
refresh chain must also pass. A source test or generic CLI symlink is not
installed-worker acceptance.

Host-bound regression: runner-3 job 5903f12f-eb54-4fcc-a228-ae62426aeaa3.
Unchanged baseline reproduced exit 127 for both normal and inline nested
child chains. The repaired chains and related lease/environment checks
passed three times (0.286 s). The earlier aa54ab12 baseline failed to compile
and is retained as an invalid comparison, not regression proof.
