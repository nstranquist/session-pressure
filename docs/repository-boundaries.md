# Repository ownership and delivery

Session Pressure is an independent product in `~/tools/session-pressure`.
`~/dev/nicos-tools` is its factory integration, not its delivery queue.

| Change | Source of authority | Delivery |
|---|---|---|
| Sampling, policy, admission, work coordination, identity, resident, CLI, local API, product telemetry | This repository | Guarded direct commit; verified product release bundle |
| Native app, privileged trace client/helper, signing and app installation | `apps/SessionPressure` here | Guarded direct commit; canonical app Makefile and atomic app installer |
| GPUI preview | `apps/pressure-ui` here | Product source and its documented build |
| `ndev session pressure` compatibility routing | nicos-tools | Factory commit delivery |
| Factory telemetry registration, shared telemetry query/writer machinery | nicos-tools | Factory commit delivery |
| Factory release/build tooling and compatibility Make targets | nicos-tools | Factory commit delivery |

The old `nicos-tools/apps/desktop/ndev-pressure` Makefile forwards to the
canonical app. It must inherit product signing defaults. Its preserved source
is not the authority for new product behavior. Do not add a second engine,
policy implementation, native UI, or trace implementation there.

Before editing or naming a blocker:

1. Resolve the repository and inspect the actual build/install target.
2. Put product behavior in the product; put adapters and shared factory
   infrastructure in nicos-tools.
3. Name the exact dependency and installed artifact affected. A factory delivery
   can block an updated factory wrapper; it does not block a direct product app
   build, installation, or trace test.
4. Publish each repository through its own guarded path. Do not run a factory
   delivery waiter as a product prerequisite.
5. Report source tests, committed source, installed artifacts and live results
   separately. Accepted factory deliveries are not landed or installed.

Heavy Go checks run on the configured gate runner. Swift checks run through
`session-pressure work run`. The native app uses
`make -C apps/SessionPressure test` and its canonical install targets. Keep
signature verification, the atomic install expectation, helper client checks,
PID start identity and administrator authorization intact.

An interactive trace invalidates an idle resource cohort. Start a new
artifact-bound idle observation after the final installation and interaction.
Observe long windows in the background; do not block other product work on
elapsed time or transfer time from a different artifact.
