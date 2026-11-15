# Priority admission during WORK-711

Owner: codex/01a1c4cf-ed34-74e2-8c85-23eb3a71fc62.
Status: 2026-11-15, cause and help correction verified.

The 14:09:27 UTC benchmark operation 40531bbb23b0b4e07698b8828a800b18
entered with weight 6, used 7 and capacity 8. Its acquired event reports
used 13 and decision_reason=priority_override_beyond_capacity. This is an
intentional policy path, not a duplicated weight in the receipt. The live
policy enables work_limits.allow_override_beyond_capacity. README records
the operator decision on November 10. No policy was changed for WORK-711.

The installed CLI release manifest identifies revision 14c7d24208f60b4d0b2cb2a10af09f7f7c2352f9.
Its session-pressure hash matches the manifest:
4767df906cd10f594c9b26089f49101876326b7ad9ce55b6c98ba77c18b1b9f8.
Historical work events carry their own source_revision, which can differ
from this later installed release. The explicit selection reason and live
policy agree with selectOverriddenWorkWaiter and its capacity-enabled test.

The CLI help incorrectly promised that priority and operator overrides
always preserve the weighted limit. The correction names the policy switch
and retains the separate host-pressure and storage gates, active leases and
operator order. It changes no admission behavior or configuration.

The previous benchmark remains contended evidence. A new ordinary
benchmark-exclusive operation 261ca92f459ca97bec5dc38666dda935 waited ten minutes
without priority and timed out without running. At 14:46 UTC the host was
red, with 172.6 MB/s swap-out and 86.7 percent CPU. A browser waiter owned
the protected pressure reservation. No peer was canceled or reprioritized. Exclusive admission alone cannot establish a quiet host:
uncoordinated CPU and swap activity must also be reported.

Private proof: session workspace inbox-note-regression/
admission-anomaly-history.json, pressure-followup-baseline.json,
host-followup-baseline.json and quiet-admission.log. Source tests and the
final comparison outcome are recorded with the WORK-711 inbox corpus.

Runner 3 job f406ef4f-494d-45c2-959a-583a31e1699a passed the scoped
sessionpressure and sessionpressurecmd priority, override and help tests
with count=1. The previous packaging attempt failed before any tests
because the local Python tarfile API lacked extractall(filter=...). The
retry used git archive piped to tar and verified go.mod before submission.
The correction is source help only. The installed release described above
was not replaced or restarted.
