---
target: analysis
total_score: 18
p0_count: 2
p1_count: 3
timestamp: 2026-09-11T10-32-02Z
slug: internal-web-static-analysis-html
---
# Critique: the analysis screens

Measured in Firefox at 1440 on a game that declares traits and one that
declares none. Recorded after the fact from the review's own report.

## Design health: 18/40

The lowest of the five, on code written an hour earlier with no polish
pass.

## Priority issues, as found

- **P0** The orphans verdict was one sentence for all three modes and
  false in two of them: "11 entities stand alone" over a row with two
  incoming edges.
- **P0** `walkedLine` read four of the eleven run facts the engine sends,
  under a comment claiming it read whatever the result carried. No
  denominator, no seed count, no gate set.
- **P1** The verdict — the sentence a person pressed a button for — was
  `--muted` at body size, identical to the disclaimer above it.
- **P1** The read-only notice claimed "an agent runs these checks" on a
  page with three Check buttons, while the route detail, the one screen
  with a write, carried no notice at all.
- **P1** Six multi-column lists wore none of `headerRow`, `.scroller` or
  `.wide` — the three patterns the catalogue built for exactly this.
- **P2** The route detail rendered the same four keys twice, 220px apart.
- **P2** The refusal most designers meet first had thirteen vocabulary
  words, one tool name and no link out of itself.

## What was working

The cycle row; `semantics_undeclared` handled once at page level with the
engine's own generated advice; the chips as the product's chip
vocabulary with the engine's real three modes.

## Fixed in

`fc43c8d`.
