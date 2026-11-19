# App split panes clear the sidebar

Owner: claude/0625c4e3-33f9-4df9-a21f-8a454fe1f832.
Status: landed 9f6bc81 and pushed to origin main, 2026-11-19.

## Symptom

Nico reported that some pages did not show properly. On Work Queue the detail
column was blank. Only the card borders, the capacity bar and part of the
`bench-exclusive 1000` weight chip were visible at the sidebar edge. Every
left-aligned label was hidden.

## Cause

Work Queue and Process Incidents put their panes in `HSplitView`. On macOS 26
the detail column of `NavigationSplitView` runs under the floating sidebar, and
SwiftUI content is inset by the safe area. `HSplitView` is an `NSSplitView`. It
does not keep that inset after a pane is added or removed. An offscreen layout
probe reproduced it: the queue label sat at x=248 (clear of a 220 pt sidebar),
moved to x=209 when the detail drawer opened, and stayed at 209 after the
drawer closed. The same probe with an `HStack` split kept the label at 248
through open and close.

## Decision

`ResizableSplit` in `Components.swift` replaces `HSplitView`. It is an `HStack`
with a 1 pt divider and a 9 pt drag target. One pane is sized (`sizedRange`) and
the other pane is flexible (`flexibleMinWidth`). Work Queue sizes its drawer
(320 to 480, default 380). Process Incidents sizes its list (290 to 560,
default 360). Do not use `HSplitView` in the detail column.

Rejected: keep `HSplitView` and add leading padding. The sidebar width changes
with user resizing and window state, so a fixed inset would be wrong at some
widths.

## Verification

- `make test`: 150 tests in 26 suites passed.
- `make install-headless`: build 9f6bc81 signed (Team ID C82D23RNQ9) and
  installed with a rollback.
- Offscreen layout probe: results above.
- Not verified: a capture of the installed window. This agent process has no
  screen-recording permission (`screencapture -l` failed), and the rules forbid
  a foreground fallback.
