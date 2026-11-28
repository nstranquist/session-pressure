# The front door links the promoted revision (2026-11-28)

## Problem

`session-pressure-release promote` reads the target commit once, installs the
CLI, helper and app from a clean clone of it, then rebuilds the recorded
front door (`scripts/release/README.md`, "Front door"). The front door's own
build linked the session-pressure checkout's `HEAD` at the time it ran. That
build takes minutes. In a checkout where other agents land commits, `HEAD`
moved during it twice in one evening:

1. A promotion of commit A installed the CLI and helper at A. A commit B
   landed during the front-door rebuild, so the front door linked B. The
   check then failed with "front door links B after republish, want A", and
   the host was left with the CLI and helper on A and the front door on B.
2. The next promotion installed B everywhere. A commit C landed during it, so
   the lag check failed with "installed binary is still 1 commit(s) behind
   HEAD", although every binary linked exactly the promoted commit.

## Change

- `frontdoor.PinEnv` (`SESSION_PRESSURE_FRONT_DOOR_PIN`) is part of the
  front-door contract. It carries the exact 40-hex commit a `Republish`
  command must link. A command that cannot link it must fail.
  `WithoutWrapperEnv` drops it with the other wrapper variables.
- `promoteFrontDoor` runs the rebuild with the promoted commit in `PinEnv`.
  The doctor run through the front door does not get it. When the front door
  still links another revision, the error names `PinEnv`.
- `checkPromotedSourceLag` replaces the inline lag check. When `HEAD` was
  promoted and the installed CLI links exactly that commit, a lag means
  `HEAD` moved after the promotion started. The promotion succeeds, the
  receipt's `head_moved_to` names the new `HEAD`, and the command asks for
  another promotion. Any other lag still fails.
- The front door used on the primary Mac reads `PinEnv`, removes it from its
  environment so the builds it starts do not inherit it, and archives the
  session-pressure replacement at that commit instead of `HEAD`. It refuses
  the pin for modes that are not a native host install, and fails when it
  has no session-pressure replacement to pin.

Not changed: a promotion of a ref other than `HEAD` still reports
`not-republished-ref-not-head` and leaves the front door alone. The front
door builds its own `HEAD`, which need not compile against an older
session-pressure, and a front door whose rebuild predates the pin would link
`HEAD` anyway.

## Decisions

- Pin the rebuild instead of checking `HEAD` again after it. A second check
  only reports the drift after the front door is already rebuilt on the
  wrong commit. Promoting again until `HEAD` holds still does not converge in
  a busy checkout: the second promotion above went behind within a minute.
- Treat a moved `HEAD` as success only when the installed CLI links exactly
  the promoted commit. A newer front door that contains the promoted commit
  is not accepted, because the binaries would still disagree.
- Pass the pin in the environment and not as a flag. The recorded `Republish`
  argv belongs to the front door, and promotion must not assume its flags.

## Verification

- `go test ./scripts/release/ ./frontdoor/` passes. New tests:
  `TestPromoteFrontDoorPinsThePromotedRevisionWhenHeadMoves` (a rebuild that
  honours the pin stays on the promoted commit while its `HEAD` moves; one
  that ignores it fails and names the pin) and
  `TestCheckPromotedSourceLagAllowsOnlyAMovedHead` (a moved `HEAD` with an
  exact install passes; an install that stayed behind, a lag with no
  revisions, or both together still fail). The republish test checks that
  only the rebuild gets the pin.
- The front door's tests pin a sibling replacement while the sibling's
  `HEAD` moves, reject a pin that is not an exact commit, reject a pin with
  no replacement, and check that the pin is removed from the environment.
