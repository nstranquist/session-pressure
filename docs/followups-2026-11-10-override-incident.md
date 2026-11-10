# Follow-ups from the 2026-11-10 override incident

Filed 2026-11-10 (not built). Context: at ~00:50-01:00 CT the work pool sat
at weight 10 of capacity 8 and a weight-2 waiter with an operator override
requested still could not admit, because the per-waiter override only
reordered the queue. It died at its wait bound unleased. The mitigation that
shipped the same night is `work_limits.allow_override_beyond_capacity`
(commit defd6e5; live policy set ON). These two items are the root causes
the toggle does NOT fix.

## 1. Telemetry self-improve is classified heavy (weight 6) at ~4% CPU

The telemetry-self-improve lease held weight 6 of the 8-weight pool for 42+
minutes while using about 4% CPU. A heavy weight priced for a full build is
wrong for a mostly-idle telemetry job: it starves real builds/tests and,
with the override toggle now on, its cost is hidden rather than surfaced.

Ask: audit the class/weight the telemetry self-improve workload requests
(or is assigned) and reclassify to its measured profile, or add demotion
evidence to `work report` so a leased weight far above measured CPU/RSS is
flagged automatically. Evidence source: `session-pressure work history
--since 24h` plus the incident snapshot in
`~/.nicos-dev/session-pressure/work-leases.json` from 2026-11-10 ~00:57 CT.

## 2. Parked leases whose supervisor died are not reclaimed

An express-build lease (weight 2) sat parked with its supervisor process
dead - pid reparented to init, 0.46s CPU in 16 minutes - still holding its
weight. Nothing reclaimed it, so 2 of 8 pool weight was dead capacity
during the incident. (Left in place deliberately during the incident
response; it is this follow-up that owns reaping it.)

Ask: the guard should detect a parked/leased operation whose supervisor
pid is gone (or reparented with no live owner) and release the lease with
an auditable event, the way dead-owner leases are already pruned for other
states. Cover both the parked state and any leased state whose owner
process tree has exited without releasing.
