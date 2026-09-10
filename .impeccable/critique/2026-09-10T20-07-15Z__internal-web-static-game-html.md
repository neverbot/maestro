---
target: the game home
total_score: 25
p0_count: 2
p1_count: 2
timestamp: 2026-09-10T20-07-15Z
slug: internal-web-static-game-html
---
# Critique: Maestro game home (`/g/{slug}`)

Measured in Firefox at 1680, 1280, 970 and 500px on `le-mans` (almost
empty) and `interface-e2e` (1025 entities, four views, one document).
Every number came from `getBoundingClientRect` / `getComputedStyle`.

## Design health: 25/40

| # | Heuristic | Score | Key issue |
|---|---|---|---|
| 1 | Visibility of system status | 2 | The home is the only shell with no trail; `document.title` is "Maestro" on every page; no destination marked on the home. |
| 2 | Match system / real world | 3 | Two empty states end in "through this instance's API… content routes", developer vocabulary aimed at a designer who has no API. |
| 3 | User control and freedom | 3 | The sticky switcher is a real way out. Docked: below ~700px the header collapses and it becomes unusable. |
| 4 | Consistency and standards | 1 | Seven row heights on one screen; `--raised` declared and dead; `border-radius: 4px` against a 3px token; three type roles unimplemented; every destination has two spellings and Prose a third. |
| 5 | Error prevention | 3 | `whoWrites()` tells a viewer the write will be refused before they try. |
| 6 | Recognition over recall | 3 | Name, key and count on every row; the switcher names the current game. |
| 7 | Flexibility and efficiency | 2 | No search, no filter, no shortcut, though the frame's Navigation section says the bar holds a search field. |
| 8 | Aesthetic and minimalist design | 2 | A floating 1440px header band, a bordered box holding one top-aligned row, an orphan "Images" link 250px below the content. |
| 9 | Error recovery | 3 | `.state.refused` in `--danger` carrying the server's own sentence. |
| 10 | Help and documentation | 3 | One honest onboarding link, pointing at a documentation site the repo says does not exist. |

## Anti-patterns verdict

Not slop. No icon sidebar, no counter cards, no dark-blue chrome, no
GitHub tab strip, no hero. Links are ink, not blue. The accent appears
once per screen. The failure is different: **the document describes an
interface that was not built.** Three of seven type roles were
unimplemented, `:root { font-size: 0.875rem }` silently detuned every
`rem` in the stylesheet, and there was no `box-sizing: border-box`
anywhere, so the frame's own headline number was wrong by 48px.

Deterministic scan (`npx impeccable detect`): one advisory, `rgb(0,0,0)`
on the noscript paragraph, a colour outside the palette.

## What is working

1. **The negative-state component is exactly what the spec ordered**, and
   `whoWrites()` conditioning the copy on the caller's role is the best
   piece of writing in the interface.
2. **The achromatic chrome holds**, verified on screen across both games:
   the only chromatic pixels are the current destination's underline and
   an invalid count.
3. **One summary call carries the whole page.** 1025 entities render in
   the same shape and the same time as 3.

## Priority issues

- **P0 The header was not the page frame.** It sat inside the body's
  1440px cap and 24px padding: a paper band from x=120 to x=1560 in a
  1680px window with a bottom rule ending in mid-air. *Fixed*: the bar
  spans the window, a `.header-inner` row carries the cap, and every
  shell wraps its content in `<main class="page">`.
- **P0 Rows had seven heights and their content sat at the top.**
  36/37/43/44/44.1/45.1/66.1px on one screen; on `le-mans` the "Circuits"
  row put 21px of content at the top of 36px with 14px of paper below.
  *Fixed*: `box-sizing`, `align-items: center`, a fixed 20px line box, and
  `flex-wrap: nowrap` in a lane.
- **P1 The home had no trail and no page head.** *Partly fixed*: the tab
  is named and the trail was judged wrong for the home (a one-item trail
  duplicated the `<h1>`); the page head rule is not built.
- **P1 `:root { font-size: 0.875rem }` detuned every rem.** `1.6rem` meant
  22.4px and `2rem` meant 28px, off the mandatory scale. *Fixed*: the size
  moved to `body`.
- **P2 Three type roles unimplemented at the top of the hierarchy.**
  *Fixed*: `h1` and `h2` are the serif at 28px/22.4px.
- **P2 `--raised` was declared and dead**, overridden two lines later in
  the same block, on the one component the design names for it. *Fixed*.

## Still open

- The onboarding link points at a documentation site that does not exist.
- Two empty states explain the MCP API to a person who has no API.
- No search field anywhere, though the frame says the bar holds one.
- Narrow widths verified in the CSSOM, not rendered: this browser will not
  resize.
