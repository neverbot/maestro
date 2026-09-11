---
name: Maestro
description: A warm-paper workbench for reading, judging and correcting the design of a game.
colors:
  paper: "#f7f3e9"
  ground: "#efe9dc"
  raised: "#fcfaf4"
  ink: "#2c221b"
  muted: "#6e625a"
  line: "#d8d2c7"
  line-strong: "#8e8279"
  accent: "#9c470d"
  accent-ink: "#fbf8f2"
  accent-soft: "#fae1d1"
  danger: "#a72629"
  data-1: "#96311d"
  data-2: "#1086d3"
  data-3: "#bf7101"
  data-4: "#6e4fe9"
  data-5: "#4e925a"
  data-6: "#7a358c"
  data-7: "#028f95"
  data-8: "#8e3256"
typography:
  display:
    fontFamily: "ui-serif, Georgia, Times New Roman, serif"
    fontSize: "1.75rem"
    fontWeight: 600
    lineHeight: 1.15
    letterSpacing: "-0.015em"
  headline:
    fontFamily: "ui-serif, Georgia, Times New Roman, serif"
    fontSize: "1.4rem"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  title:
    fontFamily: "system-ui, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: 1.35
    letterSpacing: "normal"
  body:
    fontFamily: "system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.5
    letterSpacing: "normal"
  prose:
    fontFamily: "ui-serif, Georgia, Times New Roman, serif"
    fontSize: "0.95rem"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "normal"
  label:
    fontFamily: "system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: "0.03em"
  mono:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "0.78rem"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "normal"
rounded:
  sm: "3px"
  md: "5px"
  pill: "999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "24px"
  xxl: "32px"
  gutter: "24px"
  rail: "280px"
  page-max: "1440px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "6px 13px"
    height: "32px"
  button-primary-hover:
    backgroundColor: "#1d1611"
    textColor: "{colors.paper}"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "6px 13px"
    height: "32px"
  button-ghost-hover:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.ink}"
  input:
    backgroundColor: "{colors.raised}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "5px 9px"
    height: "32px"
  chip:
    backgroundColor: "transparent"
    textColor: "{colors.muted}"
    typography: "{typography.body}"
    rounded: "{rounded.pill}"
    padding: "3px 10px"
  chip-selected:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.accent-ink}"
    typography: "{typography.body}"
    rounded: "{rounded.pill}"
    padding: "3px 10px"
  panel:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "{spacing.md}"
  table-row:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "8px 11px"
    height: "36px"
  table-row-hover:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.ink}"
  table-row-compact:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "4px 0"
    height: "28px"
  page-header:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "0 {spacing.gutter}"
    height: "48px"
  breadcrumb:
    backgroundColor: "transparent"
    textColor: "{colors.muted}"
    typography: "{typography.body}"
    height: "32px"
  negative-state:
    backgroundColor: "transparent"
    textColor: "{colors.muted}"
    typography: "{typography.body}"
    padding: "{spacing.lg} 0"
    width: "62ch"
---

# Design System: Maestro

## 1. Overview

**Creative North Star: "The Workshop Notebook"**

Maestro looks like the bound working notebook of a design team, opened
flat on a desk in daylight. Warm paper, ink that is brown-black rather
than black, a rust accent that reads as something drawn on the page
rather than something rendered on a screen. It is a reading surface
first: a designer comes here to judge several hundred rows of content an
agent wrote, and everything the interface does either helps that reading
or is in its way.

Density is high and unapologetic. Rows are 36 pixels, the type scale is
compact, and hierarchy is earned with weight, size and rule lines rather
than with air. Two typefaces carry the whole product and they divide the
work by ownership: the tool speaks sans and the game speaks serif, in
the reader's own system faces. A quest title, a place name and a
paragraph of lore are set in the serif because they belong to the world
being designed; a column
header, a filter and a button are set in the sans because they belong to
Maestro. That split is the single most legible thing about the system.

This explicitly rejects what `docs/product.md` names: the generic admin panel,
the dark blue SaaS dashboard, GitHub's chrome, the ad-choked game wiki
and the empty beautiful landing page. It is not a dashboard, it has no
counter tiles, and it is not decorated. What is on screen is content.

**Key Characteristics:**

- Warm paper ground, never white, never grey.
- Two voices: sans for the tool, serif for the game.
- Rust accent, spent sparingly and never as decoration.
- Flat surfaces, three shadow levels reserved for what genuinely floats.
- 36-pixel rows and a compact scale: comparison beats comfort.

## 2. Colors

Tinted paper and brown-black ink, with a single earth accent that looks
like it came out of the same material as the page.

### Primary

- **Kiln Rust** (`#9c470d`): the one accent. It marks focus, selection,
  the current item and the single active filter, and nothing else. It
  never fills a button and never carries a mood. At 5.7:1 on paper it is
  legible as text, which is why the selected filter chip can wear it.
- **Rust Wash** (`#fae1d1`): the accent at reading weight, for the
  background of a selected row or a highlighted search match. Never for
  text.

### Neutral

- **Ledger Paper** (`#f7f3e9`): the reading surface. Panels, tables, the
  header bar. This is where content lives.
- **Desk** (`#efe9dc`): the ground behind the paper, one step darker.
  It is what a row turns on hover and what sits under the diagram
  canvas. The diagram is a thing lying on the desk; the panels are the
  paper on top of it.
- **Leaf** (`#fcfaf4`): the raised surface, one step lighter than paper,
  for what is genuinely lifted: an open menu, the game switcher, a panel
  over the canvas.
- **Bister Ink** (`#2c221b`): all primary text, and the fill of the one
  primary button. Brown-black, never `#000`. 14:1 on paper.
- **Sepia** (`#6e625a`): secondary text, column headers, counts,
  timestamps, and the word for an absent value. 5.3:1 on paper.
- **Hairline** (`#d8d2c7`): decorative rules. Table rules, panel edges,
  input strokes. Deliberately below 3:1: it is decoration, not signal.
- **Stroke** (`#8e8279`): a line that *is* the signal. The dashed
  outline of a node whose colour slot found nothing, the dashed border
  of a read-only notice. Held to 3:1 against the ground.

### Tertiary

- **Alarm** (`#a72629`): the only other chromatic exemption. It means
  "this needs your attention" and appears on form errors, invalid rows
  and destructive confirmations. It is never used for a "stop" or a
  "no", only for a fault.

### Data

Eight categorical hues, assigned by hashing a value's text, used **only
inside a diagram or a legend** and never in the chrome. Chosen by search
under three simultaneous constraints: at least 3:1 against the ground,
at least 28 degrees apart on the hue wheel, and pairwise separable under
both deuteranopia and protanopia. They ship as `data-1` to `data-8` and
are re-validated by test, not by eye.

### The dark set

The product ships two themes and answers to the reader's system
setting: `prefers-color-scheme: dark` swaps the whole token set. It is
**not an inversion**. The ground becomes a warm near-black rather than a
blue-black, which is the same argument the light set makes about paper,
and the eight data hues are a second set tuned separately at the
lightness a dark ground needs, in the same order, so a legend read in
one theme reads the same in the other. Eight hues inverted mechanically
give two that vanish and two that glow.

| Role | Light | Dark |
|---|---|---|
| paper | `#f7f3e9` | `#1c1913` |
| ground | `#efe9dc` | `#13100b` |
| raised | `#fcfaf4` | `#26221c` |
| ink | `#2c221b` | `#e7e2d9` |
| muted | `#6e625a` | `#9a9289` |
| line | `#d8d2c7` | `#36312a` |
| line-strong | `#8e8279` | `#81776d` |
| accent / focus | `#9c470d` | `#d78958` |
| danger | `#a72629` | `#e67a73` |

Measured on the dark set: ink on ground 14.71:1, muted on ground 6.19:1,
danger on ground 6.68:1. `internal/web/static_tokens_test.go` holds both
sets, and a value changed in one theme and not the other is a test
failure, not a discovery made by a reader in the dark.

### Named Rules

**The Two Exemptions Rule.** The chrome is achromatic. Exactly two
chromatic tokens exist outside the data palette, `accent` and `danger`,
and a third is admitted only against this test: not "would a colour
help" but "is there a fact on screen that nothing else says". Colour is
a channel the diagram already spends; the interface does not compete for
it.

**The Ink Button Rule.** The primary button is ink on paper. A button
filled with the accent is forbidden: it would spend the accent on a
fourth meaning and make every screen with a form the loudest screen in
the product.

**The Two Grounds Rule.** `ground` is darker than `paper` on purpose,
and never the other way round. The diagram lies on the desk; the reading
panels are sheets on top of it.

## 3. Typography

**Serif (the game's voice):** `ui-serif, Georgia, "Times New Roman", serif`
**Sans (the tool's voice):** `system-ui, sans-serif`
**Mono (anything copyable):** `ui-monospace, SFMono-Regular, Menlo, monospace`

**Nothing is downloaded.** The product ships no web font. Literata and
Fira Sans were the drawing board's choice and are named here as the
intent, not as what renders: a self-hosted pair costs 159 KB of latin
subsets against a payload budget of 150 KB, and the CSP that admits no
third-party origin rules out a font CDN besides. What renders is the
reader's own system faces, which on macOS are New York and SF Pro.

**Character:** What survives the substitution is the *split*, and the
split is the system: a serif for what belongs to the game, a sans for
what belongs to Maestro. Every platform's system serif is a text face
built for long reading and every platform's system sans is drawn for
small sizes on screen, so the contrast between the two voices holds
wherever the product is opened. The specific warmth of Literata does
not, and a future decision to raise the payload budget by those 9 KB
would change the faces without changing a single rule below.

### Hierarchy

- **Display** (serif 600, 1.75rem, 1.15): the page title, once per
  screen. The name of the thing being looked at.
- **Headline** (serif 600, 1.4rem, 1.25): section headings and the
  name of an entity in its own detail view.
- **Title** (sans 600, 1.125rem, 1.35): panel headings and group
  titles inside a list.
- **Body** (sans 400, 0.875rem, 1.5): everything the tool says.
  Table cells, form fields, buttons, navigation.
- **Prose** (serif 400, 0.95rem, 1.6, max 68ch): the game's own
  writing. Lore, mission text, documents. The line length cap is not
  optional.
- **Label** (sans 600, 0.75rem, 0.03em): column headers, rail
  headings, metadata keys. Sentence case, never uppercase.
- **Mono** (mono 400, 0.78rem): keys, slugs, JSON pointers,
  version numbers. Anything a person might copy.

Every adjacent step is at least a 1.24 ratio apart. A flat scale is what
made the previous interface unreadable and is the specific fault this
one exists to fix.

### Named Rules

**The Two Voices Rule.** The tool speaks sans, the game speaks serif. A
quest title, a place name, an entity name and every line of game prose
are serif. A column header, a button, a filter and a count are
sans. If it would survive the game shipping, it is serif.

**The No Uppercase Rule.** Labels are sentence case with light tracking.
All-caps labels are the admin-panel tell, and they cost legibility at
the size labels are actually set.

**The Copyable Is Mono Rule.** If a value is a machine identifier a
person might select and paste, it is monospace. If it is a name a person
wrote, it is not.

## 4. Elevation

Surfaces are flat at rest and depth comes from tone: `ground` behind,
`paper` for content, `leaf` for what is lifted. On top of that there are
exactly three shadows, and all three are tinted toward the paper's own
brown rather than black. A neutral black shadow on a warm ground is what
makes an interface read as a dashboard, and it is the specific failure
this palette is built to avoid.

### Shadow Vocabulary

- **Resting** (`box-shadow: 0 1px 2px rgba(94, 72, 55, 0.12)`): the
  barest separation of a panel from the ground. Static surfaces.
- **Lifted** (`box-shadow: 0 2px 9px rgba(94, 72, 55, 0.14)`): something
  the user opened. The game switcher, a dropdown, a panel over the
  canvas.
- **Floating** (`box-shadow: 0 10px 30px rgba(94, 72, 55, 0.18)`): a
  surface over the whole page. Used at most once per screen.

### Named Rules

**The Warm Shadow Rule.** Every shadow is `rgba(94, 72, 55, …)`, the
paper's own brown. `rgba(0, 0, 0, …)` is forbidden everywhere.

**The Earned Elevation Rule.** A shadow answers a question about
stacking, never about importance. If nothing is underneath it, it is
flat.

## 5. Components

### Buttons

- **Shape:** barely rounded (3px), 32px tall.
- **Primary:** Bister Ink fill, Ledger Paper text, 6px by 13px padding,
  resting shadow. One per screen, on the single most likely action.
- **Hover / Focus:** hover darkens the fill to `#1d1611`; focus draws a
  2px Kiln Rust ring at 2px offset. Both transition in 120ms
  ease-out-quart.
- **Ghost:** transparent, ink text, Stroke border. Every secondary
  action. Hover fills with Desk.
- **Link button:** no box at all, ink text with a 2px-offset underline.
  For an action that reads as a sentence.

### Chips

- **Style:** pill, 1px Hairline border, Sepia text, transparent fill.
- **State:** the selected chip fills with Kiln Rust and takes
  `accent-ink` text, the near-paper tone that stays legible on it. Rust
  Wash is a background, never text, including here. Exactly one chip in a group is ever selected; a multi-select
  group uses checkboxes, not chips.

### Panels

- **Corner Style:** 3px.
- **Background:** Ledger Paper on the Desk ground.
- **Shadow Strategy:** resting only. See Elevation.
- **Border:** 1px Hairline.
- **Internal Padding:** 12px, and 16px only where prose is the content.
- Panels do not nest. A panel inside a panel is a defect.

### Inputs

- **Style:** Leaf fill, 1px Hairline border, 3px radius, 32px tall.
- **Focus:** border becomes Kiln Rust and a 2px ring is drawn at 2px
  offset. No glow, no shadow change.
- **Error:** Alarm border and an Alarm message directly beneath, never a
  tooltip.
- **Placeholder:** Sepia, and never a substitute for a label.

### Tables

The signature surface of this product and the one to get right.

- **Row:** 36px, uniform. 11px of horizontal padding and a fixed 20px
  line box rather than vertical padding, because a row carrying a tag
  chip is otherwise one pixel taller than a row without one, and a
  one-pixel wobble is what makes a table look unmade.
- **Header:** Label type, Sepia, sticky under the page header, one
  Hairline beneath.
- **Hover:** the row turns Desk. No lift, no border change.
- **Target:** the whole row is the link, through a pseudo-element on the
  name that covers it, so the row keeps one anchor and one accessible
  name. Only a genuine control inside the row is raised back above it. A
  hover that lights the whole row over a link occupying a third of it is
  a promise the row does not keep.
- **Numbers:** right-aligned, `font-variant-numeric: tabular-nums`.
- **Name cell:** the entity name in the serif at 600 with its slug **inline**
  after it, in Mono at Sepia. Stacking the slug underneath costs 17px a
  row and takes a 900px window from 17 rows to 9.
- **Absent value:** the word for what is missing, in Sepia italic, for
  example "no zone". An empty cell is forbidden: it cannot be told from
  a value that failed to load.
- **Container:** one border and a 3px radius around the whole table,
  rules between rows, and `overflow-x: auto` on the wrapper with a
  `min-width` on the table. Rows never wrap into two lines to fit.
- **Columns:** the fields the content actually has. A name and a key are
  the two things the reader already knew.

### The page frame

The one structure every screen is built in, decided once and stated
here.

- **Content width:** 1440px maximum, centred on a full-bleed ground,
  24px gutters and 16px below 780px. The 68ch measure belongs to the
  prose role alone and never to the page: it was on `body` once, and it
  squeezed the catalogue, the diagram and a pair of selects into 686px
  of a 1280px window.
- **Columns:** content and a 280px rail, 24px apart, the rail sticky
  under the header and folding beneath the content below 1100px. The
  rail carries what is true of the whole screen, never the screen's own
  content.
- **Order in the header, left to right:** wordmark, game switcher,
  destinations, spacer, sign out. One row, 48px, sticky. The account has
  no screen of its own yet, so nothing in the bar names the person
  signed in.
- **Below it:** a 32px breadcrumb, then the page head — title, count,
  read-only notice — closed by a 1px rule and 24px of space.
- **Wide content scrolls inside its own container**, never the page. A
  table sets `min-width` and its wrapper `overflow-x: auto`, so a row
  stays 36px at any width instead of wrapping into two lines.

### Negative states

One component, three shapes, always in the content column, never in a
box, never with a stripe, capped at 62ch.

- **Empty:** a bold `ink` line naming what is absent, one `muted`
  sentence saying who would put it there, at most one link. Never a
  tutorial: a person reading it has no API.
- **Loading:** the same shape, rendered only after 200ms so a fast
  answer never flashes.
- **Refused:** the bold line in Alarm, the reason in plain words, one
  action.

### Navigation

- **Header bar:** 48px, Ledger Paper, one Hairline beneath. Holds the
  wordmark, the game switcher, the destinations, a spacer and sign out,
  in that order. There is no search field in the bar: search belongs to
  the catalogue it filters, and a second one here would be a control
  that searches nothing in particular.
- **Game switcher:** a `<details>` control, Leaf fill, lifted shadow
  when open, with the current game marked by `aria-current` and weight
  600 rather than by colour. It is always present on every screen inside
  a game.
- **Breadcrumb:** Body type, Sepia, with the current page in ink at
  weight 500. The game's name is always the first crumb.
- **Active state:** weight and ink, never a filled pill.

### The Read-Only Notice

A distinctive component this product needs because most of its content
cannot yet be edited by hand. A dashed Stroke border, 3px radius, Sepia
Label type, sitting at the top right of the page header: "Read-only,
written by the agent". It states the limitation where someone would look
for the button, rather than leaving them to conclude the button is
missing.

### Named Rules

**The Named Absence Rule.** Nothing on screen is blank because a value
is missing. Absent gets a word, empty gets a different word, and the two
never look alike.

**The Shadow Boundary Rule.** An element selector in the global
stylesheet does not reach inside a component's shadow root, and custom
properties do. No component writes a bare `button {}` of its own; every
one adopts the shared control stylesheet, written in terms of
`var(--…)`. This failed silently for a whole build: five of six controls
on the view screen were browser defaults, one of them a `#ffffff`
input.

**The Never Hidden Rule.** Navigation is never hidden without being
moved somewhere reachable. Below 780px the destinations join the game
switcher's menu; exactly one of the two carries them at any width, never
both and never neither.

## 6. Do's and Don'ts

### Do:

- **Do** set every game-owned word in the serif and every tool-owned
  word in the sans. This split is the system.
- **Do** keep rows at 36px and lists dense. These users read hundreds of
  rows and came to compare them.
- **Do** tint every shadow with `rgba(94, 72, 55, …)`, the paper's own
  brown.
- **Do** name absences in words: "no zone", "never reviewed", "no
  requirement".
- **Do** state read-only where the missing action would be, using the
  Read-Only Notice.
- **Do** mark the current item with weight and `aria-current`, and use
  the accent only for focus, selection and the single active filter.
- **Do** cap prose at 68ch, and nothing else.
- **Do** let wide content scroll inside its own container so the page
  never scrolls sideways.
- **Do** put a breadcrumb on every screen inside a game, and delete the
  back links it replaces.
- **Do** build every repeated pattern once, as a shared component. A
  second implementation of the same shape is a defect and gets reported
  as one.

### Don't:

- **Don't** build **the generic admin panel**: no icon sidebar, no card
  grid of counters, no page that could belong to any product.
- **Don't** reach for **the dark blue SaaS dashboard**. This is a
  daylight reading surface.
- **Don't** borrow **GitHub's chrome**: no dense grey-blue header bar,
  no tab strip across the top of a page.
- **Don't** bury content the way **the ad-choked game wiki** does. Every
  pixel of chrome is a pixel taken from the catalogue.
- **Don't** produce **the empty beautiful landing page**. These screens
  are judged full, not empty.
- **Don't** use `#000` or `#fff` anywhere, including in a shadow.
- **Don't** fill a button, a badge or a header with the accent.
- **Don't** use `border-left` or `border-right` above 1px as a coloured
  stripe on a row, panel or callout.
- **Don't** set a label in all caps.
- **Don't** nest a panel inside a panel.
- **Don't** leave a cell blank when the value is absent.
- **Don't** put a measure meant for prose on the page.
- **Don't** hide navigation at a breakpoint without moving it somewhere.
- **Don't** write a bare element selector for a control inside a
  component and expect the global stylesheet to have styled it.
- **Don't** animate layout properties, and never use bounce or elastic
  easing. Transitions are 120ms for state and 180ms for disclosure, both
  ease-out-quart.
- **Don't** add a modal before an inline or progressive alternative has
  been ruled out in writing.

**The audit test:** if a screenshot of a screen could be captioned "some
admin tool" by someone who has never seen Maestro, it has failed. The
serif in the content and the warm paper under it are what make it
answerable at a glance.
