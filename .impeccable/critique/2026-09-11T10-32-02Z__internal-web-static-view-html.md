---
target: view
total_score: 21
p0_count: 1
p1_count: 2
timestamp: 2026-09-11T10-32-02Z
slug: internal-web-static-view-html
---
# Critique: views, prose and images

Measured in Firefox at 1440. Recorded after the fact from the review's
own report. The diff could not be seen on real data — every document in
the instance has one version — so its spelling was read by injecting
`internal/markdown`'s own five classes.

## Design health: 21/40

## Priority issues, as found

- **P0** Opening "Save as…" moved the view frame 1178px down and put its
  own Cancel button below the fold; the panel had no background, no
  border and no shadow. Right-aligning a 32px ghost and right-aligning a
  1216px form are not the same decision.
- **P1** `select` was never written in `styles.css`, so the prose page's
  two version pickers were `2px inset` browser defaults in `#e9e9ed`.
- **P1** `.diff` declared no background, so it sat on the desk — and
  `.diff-added` fills with the desk colour, making one of the hueless
  spelling's four channels invisible.
- **P2** The images list has never been rendered with an image in it;
  driven with rows it measured 81px against a 36px token.
- **P2** The view page's trail never named the view, and every tab in the
  product was "View · Maestro".
- **P3** The version history was 41px boxes against the 28px compact rule.

## What was working

The negative state as one component; the control vocabulary holding
inside every shadow root; the hueless diff reading without a legend.

## Fixed in

`8de5bc8`, `e289dc7`.
