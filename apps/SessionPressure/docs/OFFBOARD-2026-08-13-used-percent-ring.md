# Offboard — used-percent overview ring (2026-08-13)

The Host Pressure ring fills to **memory used** (`100 − freePercent`). It no longer uses the discrete level stubs (normal 18%, warning 45%, red 72%, critical 92%). Policy color is unchanged. Menu-bar gauge needle follows used percent.

Math lives in `NDevPressureCore/PressureUtilization.swift`. Installed: `/Applications/NDev Pressure.app` build 3. Tests: `make test` includes `utilization is memory used, not a discrete policy-level stub`.

Session receipt: `docs/active/08-13-scratchpad-pressure-ui-review/`.
