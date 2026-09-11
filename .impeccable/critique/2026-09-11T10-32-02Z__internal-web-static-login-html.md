---
target: login
total_score: 23
p0_count: 1
p1_count: 1
timestamp: 2026-09-11T10-32-02Z
slug: internal-web-static-login-html
---
# Critique: the way in — sign in, the picker, the game home

Three screens as a set, measured in Firefox at 1440 and at a real 500px
viewport. Recorded after the fact from the review's own report: this
snapshot was not written at the time, which is why the trend starts here.

## Design health: 23/40

Dragged by the picker and the sign-in, not by the home.

## Priority issues, as found

- **P0** The ≤780px media query set the `padding` shorthand on `.page`,
  so it also zeroed `padding-top` and beat `.sign-in` at equal
  specificity: below 780px the wordmark was clipped through its
  ascenders and a game's title touched the header rule with a 0.0px gap.
  Fourth instance of that cascade shape in one design pass.
- **P1** The game switcher measured 21px tall with no border, no
  background and no padding — the only route between games, and at
  narrow widths the only carrier of the destinations.
- **P1** The picker built its own row rather than the shared one, so the
  slug sat across a 500px gulf from the name and the counts pass 4 owed
  were missing.
- **P2** `#views-more` in `game.html` was a button no code read.
- **P2** Three screens, three refusal and empty shapes; a failed sign-in
  left both fields wearing their resting border and the focus on the
  document.
- **P3** "Slug" was asked for before "Name", of a designer who is not a
  developer, with no statement of what a slug may contain.

## What was working

The negative-state component and `whoWrites()`; the achromatic chrome;
one summary call carrying the whole home.

## Fixed in

`e47015e`, `0aeda1e`.
