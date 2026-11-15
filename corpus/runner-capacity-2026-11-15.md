# Runner capacity and usage view

Owner: claude (Claude Code desktop session, 2026-11-15). Status: implemented;
installed acceptance recorded below when complete.

## Request

Nico asked for a management UI for the Hetzner runners (runner-3, reserve
runner-1, later hosts) to show whether the fleet is used well and to catch
spikes that limit capacity. He also asked whether Hetzner traffic limits call
for external storage.

## Decisions

1. Extend the existing Hetzner tab instead of building a new app. The tab
   already reads the fleet inventory and live jobs; it lacked use over time,
   limits and alerts.
2. Keep the app's contract: the Go CLI owns every SSH and file read
   (`remote capacity`), Swift only decodes and renders.
3. Read history from the runner's existing `host_samples` table (15-second
   samples, 14-day retention) instead of adding a sampler. Bucket on the host
   with `sqlite3 -readonly` so the payload stays small (at most 336 rows).
4. Count heavy-slot saturation per sample on the host (`sum(heavy>=slots)`),
   not from bucket maxima, so short bursts are not reported as hours.
5. Record NIC counters locally in SQLite because runners keep no network
   history. Rejected: installing vnstat on the runners (a host change outside
   this request) and a commit-delivery schema change plus runner redeploy.
   What would flip it: a runner-side network sampler in commit-delivery, which
   would give continuous history independent of the app.
6. Traffic terms are encoded per provider with their source, not inferred.

## Traffic finding (2026-11-15)

runner-3 is a Hetzner dedicated server (Ryzen 5 3600, 64 GB, RAID,
1 Gbit/s). Hetzner's Robot docs state 1 Gbit/s root servers have unlimited
traffic; a 10 Gbit/s uplink includes 20 TB/month. Measured since boot (6.2
days): 233.9 GB in, 55.5 GB out; 195.3 GB of the inbound arrived over the
tailnet (work sent from the Mac). Disk: 168 GB used of 996 GB. Neither traffic
nor capacity calls for external storage. A Hetzner Storage Box remains a
reasonable target for off-host backups (durability), which is a separate
decision.

## Live proof

First live 7-day read: runner-3 ran 3,288 jobs at 4.3% average CPU and
6.8% heavy-slot occupancy, yet all five heavy slots were full in 6 episodes
(about 22 minutes); 9 SETUP failures. runner-1 (reserve, no samples) ran
1,191 jobs with 48 SETUP failures.

## Verification

- Go: `go test ./internal/gaterunner ./sessionpressurecmd` pass (probe parse
  from a live-shaped fixture, truncation and invalid memory rejection,
  episodes across gaps, exact saturation seconds, reboot/reset/gap-safe NIC
  rates, peak-preserving downsampling, allowance projection, fleet fan-out
  with an unreachable reserve).
- Swift: contract test decodes a recorded live payload; store keeps the last
  good report on failure and re-reads on window change.
