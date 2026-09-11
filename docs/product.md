# Product

## Register

product

## Users

**Game designers, who are not developers.** They write missions, name
places, decide what unlocks what. They live in documents, spreadsheets
and their own heads, and they reach for Maestro when a game has grown
past what one person can hold at once: four hundred missions, a
progression nobody can verify by eye, a zone somebody renamed last week.

Their context is a long working session in daylight, Maestro open beside
an engine and a pile of prose. Not a glance at a dashboard. They read
more than they click, and what they read is often several hundred rows
or several thousand words.

**Their AI agents are the second user, and they got here first.** Agents
drive the same data over MCP and are, today, better served than the
humans: they can compose a query a person cannot. The interface exists
so a designer can see, judge and correct what an agent built, which
makes reading and trusting the primary act, not authoring.

The job: keep the design of a game coherent while it grows, and see the
shape of it.

## Product Purpose

Maestro holds the design of a game, never a running one. A game declares
its own kinds of things and its own kinds of connections, and Maestro
turns that into catalogues, diagrams, versioned prose and analysis that
answers questions no one can answer by reading: what cannot be reached,
what depends on itself, what nothing points at.

Success is a designer opening a view an agent saved, understanding it in
seconds, and spotting the thing that is wrong. Failure is a designer
looking at a correct screen and not knowing what they are looking at.
The product failed that test when this document was written, and the
redesign it started is the answer to it.

## Brand Personality

**Workshop, handmade, warm.**

A designer's workbench, not an enterprise console. Paper and materials
rather than glass and chrome. The tone is a colleague who knows the
project: plain, specific, never chirpy and never ceremonial. It says
"nothing here yet" and not "No data available".

The warmth is structural, not decorative. It comes from the ground being
paper rather than screen, from language that uses the game's own words,
and from the product admitting what it cannot do instead of hiding it.
It never comes from rounded illustrations, mascots or exclamation marks.

## Anti-references

- **The generic admin panel.** Bootstrap and Material dashboards, a
  sidebar of icons, a card grid of counters, a page that could belong to
  any product. This is where a CRUD tool lands by inertia, and it is the
  single most likely failure here.
- **The dark blue SaaS dashboard.** The reflex answer for anything
  called a tool. Maestro is a daylight reading surface.
- **GitHub's chrome.** The dense grey-blue header bar, the tab strip,
  the borrowed-from-a-code-forge look. It is the reflex answer for any
  self-hosted developer tool, and Maestro is not one.
- **The ad-choked game wiki.** Fandom and its family have the right
  domain and the worst possible execution: content buried under chrome.
- **The empty beautiful landing page.** Big type, generous whitespace,
  nothing on screen. Maestro's screens carry hundreds of rows and are
  judged full, not empty.

## Design Principles

**1. Always say where you are.** Which game, which section, how to get
back, how to go elsewhere. A designer who cannot switch games or name
the screen they are on has no map, and every other quality is wasted.
This was the first fault fixed: one header on every screen, carrying the
game switcher and the destinations, and a breadcrumb under it that
replaced four spellings of a back link.

**2. Speak the game's vocabulary, not the metamodel's.** The model is
generic on purpose; the interface must not be. A screen shows Quests and
Zones because that game declared them. The words "entity" and "relation
type" appear only where the subject genuinely is the model itself.

**3. Build it once and share it.** Every recurring pattern is one
component used everywhere, never a shape rebuilt per page. A future
developer should change a table in one place and see it change in eight.
Two similar implementations of the same thing is a defect, reported as
such.

**4. Density is respect.** These users read hundreds of rows. Padding
that halves what fits on screen costs them scrolling and costs them the
comparison they came for. Earn hierarchy with weight, size, rule lines
and space between groups, not with air between every row.

**5. Say what is missing.** Read-only is stated, not implied by a
missing button. An absent value and an empty value are different facts
and look different. What the product cannot do yet is written on the
screen where someone would look for it.

## Accessibility & Inclusion

No formal WCAG target and no certification claim. Common sense, and the
guarantees the code already carries stay: verified contrast on the token
set, eight data hues separated under deuteranopia and protanopia, and a
text twin that describes every diagram for a reader who cannot see it.
Those are tested and cost nothing to keep, so keeping them is not a
commitment to anything further.

New work is held to the same common sense: real focus states, labels on
controls, and no meaning carried by colour alone.
