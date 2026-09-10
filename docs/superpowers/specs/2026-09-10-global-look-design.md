# The global look: page frame, density, lists and negative states

**Date:** 2026-09-10
**Task:** Design pass 2 (`b2c061f8`), under the interface design pass
feature (`7794e7a6`).
**Works from:** `docs/superpowers/specs/2026-09-09-interface-audit.md`.
**Decides:** everything between a token and a page. Tokens themselves
are settled in `design.md`; this settles what is built out of them.

Every number below was measured in a browser on a working mock, not
chosen on paper. The mock lives in `.scratch/design-demo/frame.html`
with a four-width harness beside it, and it is throwaway: pass 3 builds
the real thing from this document.

## What is not up for decision

Carried forward unchanged, and listed so no later task reopens them:

- **The chrome is achromatic**, with two chromatic exemptions,
  `accent` and `danger`. The audit found the rule held on all ten
  screens. A third exemption is admitted only with its reasoning
  written down. Three attempts were argued down during the build.
- **The view frame owns every negative state a drawing can be in** —
  staleness, dropped references, an empty answer — with counts, in one
  place. Nothing outside it reinvents those.
- **`design.md` owns the tokens**: eleven chrome colours, eight data
  hues, seven type roles, three shadows, one easing.

## 1. The page frame

### The header: one row, one order, every screen

The audit found three different chromes in one game and a destinations
bar drawn *above* the product name. One row replaces all of it, in this
order, left to right:

```
[wordmark]  [game switcher]  [destinations]  ·······  [account] [sign out]
```

- **Height 48px**, sticky at the top, `paper` on a 1px `line` bottom rule.
- **Wordmark** in the serif at 1rem/600. It is the only place the product
  names itself, and it links to the picker.
- **Game switcher** is a `<details>` at control height (32px), `raised`
  fill, resting shadow, capped at 260px with an ellipsis. It marks the
  current game with `aria-current` and weight 600, never with colour. It
  is present on every screen inside a game, including the game home,
  which had no navigation at all.
- **Destinations** are text links, `muted` at rest, `ink` at 600 with a
  2px `accent` underline when current. Five of them: Catalogue, Views,
  Prose, Images, Analysis. Analysis is listed here although its screens
  do not exist yet, because the frame is decided once and the feature
  that builds them should not also be redesigning the header.
- **Account and sign out** sit right. Sign out is a ghost button, never
  the primary one: it is the least likely thing a person came to do.

**The bar never lies.** `aria-current="page"` marks the destination the
current URL belongs to, and the audit found `/assets` marking Views.
Images is a destination now precisely so that page has one to mark.

### Below the header

- **Breadcrumb row**, 32px, `muted` at 0.8rem, with the current page in
  `ink` at 500: `Game / Catalogue / Creatures`. **It replaces all four
  back links.** "Back to this game", "Back to the game", "Back to the
  catalogue" and "Back to this type" are deleted, not restyled: a
  breadcrumb says where you are as well as where you came from, and says
  it in one component instead of four strings.
- **Page head**: the title in the serif display role, the count beside
  it in `muted` with tabular numerals, and the read-only notice pushed
  right. Closed by a 1px rule, then 24px of space. The head is the only
  place a page states what it is.

### Widths

```
--page-max: 1440px      content, centred, on a full-bleed ground
--gutter:   24px        16px below 780
--rail:     280px
```

`body { max-width: 68ch }` is deleted. The measure moves to where it
belongs: the prose role alone, capped at 68ch inside the content column.
The audit showed that one line squeezing the catalogue, the diagram and
a pair of version selects into 686px of a 1280px window.

**Two columns**: `minmax(0, 1fr)` and the rail, 24px apart, rail sticky
below the header. The rail carries what is true of the whole screen and
never the screen's own content: the game's types with counts, its
relation types, the declared fields of the type being looked at.

### Breakpoints, and what happens at each

Measured on the mock at four widths, with the page never scrolling
sideways at any of them:

| Width | Rail | Destinations | Table |
|---|---|---|---|
| 1440 | beside, 280px | in the bar | fits, 1086px |
| 1100 | folds under | in the bar | fits, 1050px |
| 900 | folds under | in the bar | fits, 850px |
| 420 | folds under | **inside the switcher menu** | scrolls, 720px min |

Two rules come out of that table.

**A wide table scrolls inside its own container; the page never scrolls
sideways.** At 420px the five-column catalogue squeezed to 388px and the
name cell wrapped to two lines, so the row height stopped being a row
height. With `overflow-x: auto` on a wrapper and `min-width: 720px` on
the table, rows stay 36px at every width and the reader scrolls the
table, not the document.

**Navigation is never hidden without being moved.** The first draft hid
the destinations below 780px and put nothing in their place, which
leaves a phone with no way to change section. They join the game
switcher's menu instead, under a rule. Exactly one of the two
mechanisms is active at any width: verified as `reachable: true` at all
four, never both, never neither.

## 2. Density and rhythm

### The spacing scale

`4 · 8 · 12 · 16 · 24 · 32`, from `design.md`. The audit counted fifteen
distinct padding values and eight gap values in one stylesheet, none of
them a token. Every new value is one of six or it is wrong.

Rhythm comes from using different steps for different jobs, not from one
step everywhere: 4 inside a control, 8 between siblings, 16 inside a
panel, 24 between sections, 32 between a section and the next heading.

### Two row densities, and no third

- **Row, 36px.** Every list a person reads and compares: the catalogue,
  views, documents, images, analysis findings.
- **Compact, 28px.** Lists that are context rather than content: the
  rail's types and connections, a document's version history, a twin's
  rows.

Measured: uniform 36px across every row of the mock, tag chips included.
Getting there took a fixed 20px line box in the cell; without it a row
carrying a tag was 37px and a row without it 36, which is the kind of
one-pixel wobble that makes a table look unmade.

**What that buys.** The audited type page showed 9 rows of 2 columns in
a 686px column. The mock shows 17 rows of 5 columns in a 950px column at
the same window height. Roughly triple the content per screen, which is
the whole of principle 4.

### The rest of the vertical rhythm

- Header 48, breadcrumb 32, control 32, chip 26.
- The page head's rule and its 24px of space are the only separator
  between the head and the content. No panel around the whole page.

## 3. Type roles, assigned

`design.md` defines seven roles. This says which text is which, because
the audit found the split half-applied.

| Where | Role |
|---|---|
| Page title | display, serif |
| Section heading inside a page | headline, serif |
| Rail section heading, column header, metadata key | label, sans, sentence case |
| Entity, type, view, document and game **names** | the serif, 500, at 0.95rem in a row |
| Everything the tool says: counts, buttons, filters, breadcrumb, empty states | body, sans |
| Slugs, keys, JSON pointers, versions, field types | mono |
| The game's own writing | prose, serif, 68ch |

**The rule that decides an argument**: if the text would survive the
game shipping, it is the serif. A quest's name would; the word "Filter"
would not.

**A relation type is named, not keyed.** The audit found `preys_on` as a
heading on the entity page while the catalogue said "Preys on" for the
same thing. The label is what the page shows; the key belongs in mono
beside it, at metadata weight, the way a slug does.

## 4. What a list looks like

Most of this product is lists, and the audit found all of them rendered
as the same bordered card: a name left, a key right, 43px tall, in a
686px column, however many there are.

**A list is a table with rules, not a stack of boxes.** One container
with a 1px border and a 3px radius; rows separated by 1px `line`; hover
turns the row `ground`; no per-row border, no radius, no gap, no shadow.
Boxes cost 8px of border and gap per row and say nothing.

The row carries, in order:

1. **The name**, serif 500, with its **slug inline** in mono `muted`
   after it. Stacking the slug under the name pushed the row to 53px and
   the screen from 17 rows to 9; inline it costs nothing and reads the
   same.
2. **The columns the content actually has.** A catalogue of a declared
   type shows that type's declared fields and its most common relation,
   because comparison is why the reader opened it. The audited page
   showed name and key only, which are the two things a reader already
   knows.
3. **Numbers right-aligned** with `tabular-nums`.
4. **Absences in words**, `muted` italic: "no zone", "no prey", "never
   reviewed". Never an empty cell: an empty cell cannot be told from a
   value that failed to load.

**A plain list instead of a table** only where there is genuinely one
column: the rail, the switcher menu. Those are compact rows with a
bottom rule and no header.

**Above the list**: filter chips left, a filter field right. Exactly one
chip selected at a time, and the selected chip is the only surface in
the product the accent fills. **Below it**: one ghost button that says
how many more it will fetch, "Show 50 more", not "Show more".

## 5. Empty, loading and refused, once

Four empty states in four shapes on a single screen, one of them
carrying a `border-left: 3px` stripe the identity bans. One component
with three shapes replaces all of them, always in the content column,
never in a box, never with a stripe, capped at 62ch:

- **Empty** — a bold `ink` line naming what is absent, then one `muted`
  sentence saying who would put it there, then at most one link.
  *"Nothing here yet. This game has no saved views. An agent writes one
  over MCP."* It is one sentence, not the nine-line API tutorial the
  audit found in a 200px lane.
- **Loading** — the same shape, shown **only after 200ms**, so a fast
  answer never flashes a skeleton. No spinner alone.
- **Refused** — the bold line in `danger`, the reason in plain words,
  and one action. *"That did not load. The server answered 500 for this
  catalogue. Try again."*

An empty state that is genuinely a first-run moment may add one link,
and nothing more. It never teaches the API: a person reading it has no
API.

## 6. Two things pass 3 must not repeat

**The design system stops at the shadow boundary.** Element selectors in
`styles.css` do not cross into a Lit shadow root, which is why five of
six controls on the view screen are browser defaults, including a
`#ffffff` input the identity forbids. Custom properties *do* inherit
across that boundary, so the fix is not to give up on tokens: every
component adopts one shared control stylesheet, written once in terms of
`var(--…)`, and no component writes a bare `button {}` of its own. A
guard belongs with it, since this failed silently for the whole build:
assert in a browser test that a control inside each component reports
the token's background and radius, not the platform's.

**Declare the default before the media query.** Writing
`.narrow-only { display: none }` *after* the `@media` block that shows it
meant the menu items stayed hidden at every width, with nothing red
anywhere. Same shape as the `.diff > div` cascade defect already
recorded in `styles.css`. It was caught by measuring `reachable` at four
widths, which is the only reason it is a footnote and not a shipped bug.

## What each later pass takes

- **Pass 3** builds: the header, the switcher, the breadcrumb, the page
  head, the row, the table wrapper with its scroller, the chip, the
  three controls, the negative-state component, and the shared control
  stylesheet with its guard.
- **Pass 4** applies the frame to login, the picker and the game home,
  and gives the picker the counts the home already has.
- **Pass 5** applies it to the catalogue, and owes the type page real
  columns and a count that names the type rather than the page.
- **Pass 6** applies it to views, prose and images, and owes the view
  screen its full width, a help panel that is not permanently over the
  drawing, and legible labels on a background image.
- **Pass 7** re-opens all ten at the four widths in this document.
