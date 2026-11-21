---
title: First hour
---

# First hour

:::tip[Start here]
The Go coordinator is the authority. The app only displays and confirms actions.
:::

1. Confirm the CLI: `ndev --json session pressure status`.
2. Open NDev Pressure. Overview shows memory, admission, and top trees.
3. Use Policy only if you mean to change protection level. Observe-only does not admit or kill work.
4. Queue heavy work with `ndev session pressure work run --class <class> -- …`. Do not background heavy jobs with `&`.

Process CPU that shows `sampling` is not zero. Cleanup and automatic relief wait until the guard has current evidence.
