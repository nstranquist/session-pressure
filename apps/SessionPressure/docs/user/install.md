---
title: Install
---

# Install

NDev Pressure is the native menu-bar client for `ndev session pressure`. It does not keep its own database.

```sh
cd apps/desktop/ndev-pressure
make install
```

`make run` builds, copies to `/Applications`, and opens the app. The app shells out to `ndev --json session pressure`. Put the same `ndev` on your GUI PATH that you use in the terminal.
