# Maestro

**A workspace where game designers and their AI agents build the design
of a game together: characters, places, missions, progression, story.**

You are four hundred missions in. A zone was renamed last week and
nobody is sure what still points at it. The progression is a picture in
one person's head. Maestro is where that design lives, so it can be
read, questioned and corrected.

![A quest chain drawn from a saved view: ten quests, the edges that gate them, coloured by faction.](images/quest-chain.png)

*A saved view of a seeded example game. The picture is a query plus a
way of drawing it, and it is redrawn from the content every time it is
opened.*

## What it is for

### Catalogues

Every class, every circuit, every mission the game contains, listed
under the kind of thing it is and searchable by name or key. Sorted by
any column, paged fifty rows at a time, and a row that no longer fits
the shape its type declares says so.

### Views

A saved diagram is a question plus a way of drawing it. *The quests a
Mage can reach between level 20 and 30, coloured by zone* is a view
rather than a feature request. Six renderers draw one: a graph, a map
with real coordinates over your own background image, a layered
progression tree, a table, a timeline and nested boxes. Every one of
them also has a text twin that describes the same answer for a reader
who cannot see the picture.

### Analysis

The questions nobody can answer by reading. What no player can reach.
What depends on itself. What nothing points at. And saved routes, *Mage
levelling 1-20*, re-checked on demand to prove a progression still
holds after somebody renamed a zone.

### Prose

Lore and mission scripts as versioned markdown, attached to the
entities they describe, with a diff between any two versions and a
record of who wrote each one.

![The catalogue of one declared type: three hundred creatures with their keys and a declared field.](images/catalogue.png)

*Three hundred rows of one type in the same example game. The columns
are the fields that type declares, and the headings set the order.*

## What you can change here, and what you cannot

Most of the writing is an agent's job: declaring the kinds of thing
your game has, connecting them, filling them with content. Three things
are edited where they are read, and each one states the version it was
read at, so two saves that land together are reported as a conflict
with both values named rather than one of them quietly winning:

- an entity's name,
- an entity's field values,
- a document's body.

A view can also be composed in the browser, clause by clause, for the
half of the query language a sentence can hold. The other half is a
document an agent writes. Where a screen cannot do something, it says
so on the screen rather than leaving you to conclude the button is
missing.

## Where to go next

[What an agent is told](agents/index.html) is the bundle this instance
hands an agent: how to declare a game's own vocabulary, how to fill it,
and a worked example per genre. [Running it](running.html) is the
readme: self-hosted, open source, one binary and a Postgres.
