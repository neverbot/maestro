# Interface audit: every screen against the identity it claims

**Date:** 2026-09-09
**Task:** Design pass 1 (`7b93deb5`), under the interface design pass
feature (`7794e7a6`).
**Method:** all ten routes opened in Firefox against a local instance
with three seeded games, measured through the DOM rather than judged
from the source. No code was changed.

The identity this audits against is now written down rather than
implied: `product.md` (users, anti-references, five principles) and
`design.md` (tokens, seven type roles, nine named rules), with
`docs/design-system.html` rendering both.

The audit's own numbers came from `getComputedStyle` in the page,
because two of the findings below are invisible in a screenshot and
three of them are invisible in the source.

## What was opened

| Route | Screen | Game used |
|---|---|---|
| `/login` | Sign in | — |
| `/` `/games` | Game picker | three games |
| `/g/{slug}` | Game home | `interface-e2e` (1025 entities), `le-mans` (3) |
| `/g/{slug}/types` | Catalogue | `interface-e2e` |
| `/g/{slug}/t/{key}` | One type | `creature`, 1000 entities |
| `/g/{slug}/e/{type}/{key}` | One entity | `creature/c0000` |
| `/g/{slug}/views` | Views | 4 views |
| `/g/{slug}/v/{key}` | One view | `mage-map`, with a background image |
| `/g/{slug}/doc` | One document | `lore/westfall.md` |
| `/g/{slug}/assets` | Images | empty |

## The finding that explains most of the others

**The whole application is 68 characters wide.**

```css
/* internal/web/static/styles.css:189-195 */
body {
  margin: 0 auto;
  padding: 2rem;
  max-width: 68ch;
}
```

Measured: 686px of a 1280px viewport, 54%. On a 1920px monitor it is
36%. Every screen in the product, including the catalogue of a thousand
entities and the diagram the product exists to draw, is laid out inside
a measure chosen for a paragraph of prose.

`68ch` is the right cap for the game's own writing and `design.md` keeps
it there. It is the wrong cap for everything else. A designer who came
to compare four hundred missions is given a column that fits one, and
then a "Show more" button.

This one rule causes: the ragged three-lane home, the two-line wrapping
in every view name, the 1000-row list with no columns, the diagram
squeezed into 686px, and the select elements stretched to 686px to hold
the string "Version 1".

## Cross-cutting findings

### 1. The design system stops at the shadow boundary

`styles.css` styles `button` and `input`. Lit components render into a
shadow root, where a page-level element selector does not reach. Nobody
noticed, because every control still *looks like a control*.

Measured on `/g/interface-e2e/v/mage-map`, the most important screen in
the product:

| Control | Host | Background | Border | Radius | Font |
|---|---|---|---|---|---|
| Sign out | page | transparent | 1px solid | 4px | system-ui 16px |
| Save as… | `mst-save-as` | `#e9e9ed` | 2px outset | 0 | system-ui 16px |
| class_key field | `mst-view-frame` | `#ffffff` | 2px inset | 0 | -apple-system 13.33px |
| Unpin | `mst-canvas` | `#e9e9ed` | 2px outset | 0 | system-ui 16px |
| Clear the saved position | `mst-canvas` | `#e9e9ed` | 2px outset | 0 | system-ui 16px |
| ground field | `mst-ground` | transparent | none | 0 | -apple-system 13.33px |

Five of six controls are browser defaults. `#ffffff` is a colour the
identity forbids outright; `2px outset` is a bevel from 1996;
`-apple-system` at 13.33px is a font and a size that appear nowhere in
the token file.

This is the repository's own recurring defect, *correct in the module,
dead at the call site*, in CSS: the token layer is right, and the place
that draws the product does not read it.

### 2. Three different chromes, and one that lies

| Screen | Destinations bar | Header |
|---|---|---|
| Game home | absent | brand, switcher, sign out |
| Catalogue, type, entity, views, view, assets | present, **above** the header | brand, switcher, sign out |
| Document | absent | brand, switcher, sign out |

So the navigation appears, disappears and reappears as a person moves
through one game. Where it does appear, it sits *above* the product
name and the sign-out control, which inverts the hierarchy: the three
destinations are the least permanent thing on the page and they are
drawn first.

And on `/g/interface-e2e/assets` the bar marks the wrong place:

```
Views      aria-current="page"   href=/g/interface-e2e/views
Catalogue  (none)
Prose      (none)
```

The current page is `/assets`. A sighted reader sees the wrong tab in
bold; a screen reader is told the wrong location.

### 3. Four back links, four wordings, no breadcrumb

- Views: "Back to this game"
- Assets: "Back to this game"
- Document: "Back to the game"
- Catalogue: "Back to the catalogue"
- Type: "Back to this type"

Two of those are the same destination spelled two ways. None of them
says where the reader is; a breadcrumb would say both, once, in one
component.

### 4. Everything is a bordered card

Views, entity types, relation types, entities, relations, documents and
attachments all render as the same thing: a 1px box with a 4px radius, a
name on the left and a key on the right, about 43px tall, stacked with a
gap. A thousand entities is a thousand boxes.

`product.md` names "the generic admin panel" and its "card grid" as the
first anti-reference, and `design.md` says depth comes from tone and
hairlines. A list of names is a list, and rules between rows cost 1px
where a border costs 4 and a gap costs 4 more.

### 5. Every empty state is invented where it is used

On one screen, `/g/le-mans`, four empty states in four shapes:

| Lane | Shape |
|---|---|
| Views | filled box, 16px padding, **3px left stripe** `#857e72` |
| Entity types | bare grey sentence |
| Relation types | bare grey sentence |
| Prose | nine-line grey paragraph explaining the MCP API |

The Views one carries a `border-left: 3px solid`, which `design.md`
bans by name: a coloured side stripe is never intentional. The Prose one
is API documentation delivered in a 200px column to a person who has no
API. Three of the four say the same thing in a different voice.

### 6. The metamodel's vocabulary leaks onto the page

The catalogue names a relation type "Preys on". The entity page names
the same thing `preys_on`, in monospace, at heading size. The view page
labels a query parameter `class_key` and offers an empty box.

`product.md` principle 2 is that the interface speaks the game's
vocabulary, not the model's. Three screens speak the model's.

### 7. A count that names the page rather than the thing

The catalogue says `Creatures · creature · 1000 entities`. The type page
for the same type says `creature · 50 entities`, and the number grows as
"Show more" is pressed:

```js
// internal/web/static/pages/catalogue.js:126
say(metaEl, type.result.key + " · " + countLabel(rendered, "entity", "entities"));
```

`rendered` is how many rows are on screen. Two screens describe one type
with two different numbers, and the wrong one is on the screen dedicated
to that type.

### 8. One-off controls

- `/assets` with no images still shows "Show more images", a control
  with nothing to fetch.
- "Save as…" floats above the view frame, outside the thing it acts on,
  and is the only control on the page not inside a component.
- The canvas draws three paragraphs of help text (pinning, undo,
  last-writer-wins) in a panel over the diagram, covering roughly a
  quarter of it. It is documentation, permanently, on top of the picture.

### 9. Density has no rule

Measured heights of a list row: 43px on the catalogue, 43px on the type
list, 43px in the views list, 35px in the destinations bar, 50px in the
header. Vertical rhythm comes from fifteen distinct `padding` values and
eight distinct `gap` values in one 718-line stylesheet, none of them a
token. There is no spacing scale to be consistent with.

Likewise type: eleven `font-size` values, exactly one `font-weight`
declaration (`600`). Hierarchy has one lever and it is used twelve
times.

## What is already right, and must survive

Recording these so the next tasks do not "fix" them:

- **The chrome is achromatic.** No screen spends a hue on its own
  meaning. The rule held across all ten. The accent changes colour under
  the new system; the rule does not change.
- **The serif/sans split exists in part.** Entity, type, view and
  document names are already set in `ui-serif`, and the tool's chrome in
  `system-ui`. `design.md` makes this the system's central rule; the
  code already half-implements it.
- **A dark theme exists and is separately tuned**, not an inversion.
- **The view frame owns its negative states.** Staleness, dropped
  references and empty answers are stated inside the frame, in one
  place, with counts. Nothing outside it should reinvent them.
- **The text twin exists** for every diagram.

## Screen by screen

### `/login`
What it is for: getting in. First action: type an email.
Departures: no chrome at all, so the product's identity begins after
login. Inputs stretch the full 686px for a 30-character value. The
registration form is a second full form hidden behind a toggle in the
same page.

### `/` and `/games` — the picker
What it is for: choosing a game. First action: click a name.
Departures: three underlined links and nothing else. No counts, no dates,
no hint of what a game contains, though the home screen has all of it a
click later. "New game" is a `<details>` disclosure rendered as a small
grey triangle, which reads as less clickable than the links above it.

### `/g/{slug}` — game home
What it is for: the way into one game. First action: pick a lane.
Departures: three lanes of unequal length leave a ragged bottom edge;
lane headings are links in two lanes and plain text in two; "Images" sits
alone below all three, orphaned; every row is a card; on a game with
content the type key right-aligns, on a game with long names it wraps
under. No destinations bar here, so the navigation a person just learned
on another screen is missing on the screen they land on.

### `/g/{slug}/types` — catalogue
What it is for: what kinds of thing this game declares.
Departures: card rows; a redundant "Back to this game" under a
navigation bar that already offers it; entity types and relation types
given identical treatment though only one of them is clickable.

### `/g/{slug}/t/{key}` — one type
What it is for: every entity of a kind. First action: find one.
Departures: the count is wrong (finding 7). A thousand entities render
as a thousand cards with a name and a key, in key order, with no
search, no filter, no sort and no columns, in a 686px column. The
fields the type declares are not shown, so the one screen where
comparison is the whole point offers nothing to compare.

### `/g/{slug}/e/{type}/{key}` — one entity
What it is for: everything about one thing.
Departures: relation type keys shown raw (finding 6); one declared field
occupies 100px of height; "Relations out" and "Relations in" as
headings, which is the model's language for "what this points at" and
"what points at this"; each related entity is a card.

### `/g/{slug}/views` — views
What it is for: the saved diagrams. First action: open one.
Departures: four card rows with a name, a key and a renderer word. No
preview, no thumbnail, no dimensions, no staleness, nothing about what
the view answers. The list of pictures shows no pictures.

### `/g/{slug}/v/{key}` — one view
What it is for: the product's reason to exist.
Departures: the worst screen. The diagram is 686x601 in a 1280px
window. Five unstyled controls (finding 1). A permanent help panel over
the drawing (finding 8). A raw query parameter as a form label. Node
labels drawn directly on a busy background image with no halo, so on the
seeded map several are unreadable.

### `/g/{slug}/doc` — one document
What it is for: reading and comparing the game's prose.
Departures: no destinations bar. A fourth back-link wording. The
document's title is printed twice, once as the page title and once as
its own first heading. Two native `<select>` elements, unstyled and
686px wide, to choose between versions named "Version 1". The prose
itself is the best-set text in the product.

### `/g/{slug}/assets` — images
What it is for: the pictures a map view draws on.
Departures: the destinations bar marks the wrong page (finding 2). A
"Show more" button on an empty list (finding 8). The empty state
explains that backgrounds are uploaded on the view instead, which is
correct and makes the screen's own existence unclear.

## What the later tasks take from this

- **Design pass 2** settles the global look. It now has one to settle
  *to*: `design.md` exists. What it must decide beyond that is the page
  frame, since the 68ch body is the root cause and the replacement is a
  layout decision, not a token.
- **Design pass 3** builds the shared components. This audit names the
  ones the product needs and has none of: the page frame with its
  breadcrumb, the list row, the empty state, the section heading, and
  above all a control set that works **inside a shadow root**, since
  that is where five of six controls live.
- **Design pass 4** takes login, the picker and the home.
- **Design pass 5** takes the catalogue, and inherits the count defect
  and the missing columns.
- **Design pass 6** takes views, prose and images, and inherits the help
  panel, the unstyled controls and the label legibility.
- **Design pass 7** re-opens all ten in a browser.

Two findings are defects rather than design choices and can be fixed
without waiting for the look: the count on the type page
(`catalogue.js:126`) and the `aria-current` on `/assets`.
