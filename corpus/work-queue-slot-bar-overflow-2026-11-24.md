# Work Queue page pushed off screen by a 1024-slot runner

Owner: claude (session 46175392). Date: 2026-11-24. Status: fixed, tested,
installed.

## Symptom

The Work Queue page showed empty horizontal bands and one clipped chip
("…ive 1000", the last `bench-exclusive` weight chip) at the left edge.
The other pages looked normal.

## Cause

`SlotBar` in the Placement & offload card drew one segment per runner slot
with 3 pt gaps. runner-3 reports `heavy_slots: 1024` (the slot cap is a
ceiling there; memory admission decides). 1024 segments need about 3,070 pt,
so the card's minimum width forced the whole scroll content to about
3,100 pt. The page centred that content in an 800 pt pane, so nearly all of
it was off screen. Only this page has the card, which is why only it broke.

## Fix

`SlotBar` draws discrete segments up to 24 slots (`SlotBar.maxSegments`) and
a proportional fill above that. A busy runner keeps a visible 4 pt minimum
fill. The row text still shows the exact count (`heavy 5/1,024`).

## Proof

- New `WorkPlacementLayoutTests`: a `SlotBar` must fit a 200 pt offer at 0,
  2, 8, 24, 25, 1024 and 100,000 slots, and the placement card must fit the
  480 pt minimum Work Queue pane at 8 and 1024 slots. With the old
  behaviour forced back, the card needed 3,101 pt and the bars up to
  12,342 pt. With the fix, all pass. `swift test`: 161 tests pass.
- A windowless render of the full live Work Queue page (an `NSHostingView`
  never attached to a window, drawn with `cacheDisplay`) showed the empty
  bands before the fix and the normal page after it.

## Lesson

Any view that draws one shape per unit of a data value needs a cap. A
layout test that offers a narrow width and requires the view to fit catches
this class of bug. A fixed-frame render does not.
