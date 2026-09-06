# Deciding a game's shape

Four decisions, in the order you actually face them. Each one is cheap
on the day you make it and expensive to reverse, and the expense is
counted here in calls rather than in adjectives.

The reason this page exists at all: Maestro will accept almost any shape
you declare. Every write validates, every listing looks right, and
nothing tells you that a shape is wrong until somebody asks a question
it cannot answer. By then the game has content in it.

## Decision 1 — is this a field, or a relation?

**The rule: if any other entity in this game is, or could plausibly
become, the same thing this value names, it is a relation.**

- `min_level: 22` on a quest is a field. `22` is a number. The game will
  never have a row called 22.
- `zone: "elwynn"` on a quest is a **relation**. Elwynn is a place; the
  game has a row for it, or will. That the value is spelled like a
  string is the whole trap.

### What the wrong answer costs

**Before.** You declare `quest` with `{"key": "zone", "type": "text"}`
and seed 300 quests. Every write validates. Every row looks right in a
table. Filtering the listing by `zone = "elwynn"` even works, which is
what makes this mistake survive its first month.

**Three hundred entities later.** A designer asks for the quest map,
coloured by zone. A traversal walks relations; it cannot follow a text
field. There is no relation type to name in the query. Meanwhile
deleting a zone leaves 40 quests pointing at a key that resolves to
nothing, and nothing anywhere tells anyone — the string is still a
perfectly valid string.

**The repair is not a migration.** Declare `zone` as an entity type,
seed the zones, declare `takes_place_in`, read all 300 quests back, write
300 relations, then take the field out of the schema — which touches all
300 rows a second time, because an unknown field is an error and not a
silently dropped key. Six calls become six hundred, and the seed you
would have written on day one is the seed you now write anyway, on top
of live content somebody has been editing.

### The inverse mistake, so the rule does not over-apply

Modelling `min_level` as a relation to a `Level` entity gives you sixty
content-free rows and takes the number away. "Quests between level 20 and
30" is then a traversal instead of a range filter, because the range
operators are number operators and there is no number left to compare.
The same is true of a year, a lap count, a price and a difficulty.

The test is not "is this value shared by many rows" — plenty of numbers
are. It is "does this value name a **thing** the game has, or wants, its
own row for".

## Decision 2 — which end carries the data?

Data that belongs to the *connection* goes on the connection. Edges
declare their own fields, exactly as entities do.

A locked door between two rooms is the standard case. The required
ability is not a property of either room: put it on `room_a` and the
door in the other direction is wrong; put it on `room_b` and every other
way in is wrong too. Put it on the edge and it is right once.

The cost of getting this wrong is that the value has to be duplicated on
every row at one end and then kept in step by hand, forever, with nothing
checking it — and the day two of the copies disagree there is no
mechanism that can say which was meant.

## Decision 3 — one relation type with a field, or two relation types?

**The rule: if the two edges would ever be walked, counted or analysed
separately, they are two types. If they differ only in something a human
reads, one type with a field.**

A `requires` type carrying `kind: enum[hard, recommended]` is **one**
edge as far as every traversal is concerned. Every future question of
the form "what actually gates this content" then has to filter that enum
out by hand, in every query, forever — and the first author who forgets
draws a progression diagram with the recommendations in it and does not
notice, because it looks exactly like a progression diagram. Two types
cost one extra declaration on the day you seed.

**The counter-example, so the rule does not become "two types always".**
A metroidvania's `connects_to` carrying `requires_ability: text` is
**one** type. A gated door and an open door are the same spatial edge:
they are walked the same way, they belong in the same map, and a
predicate on the edge's own field separates them whenever a query wants
them apart. The difference between this case and the one above is
whether the distinction changes how the graph is *walked* or only which
edges are *selected*.

There is a third reason, and today it is a hard one rather than a matter
of taste. A relation type declares `analysis_traits` — how its edges
**behave** in a graph walk, which is what lets an engine say anything
about a game whose vocabulary it was never taught. Traits are declared
per relation type, so two edges that behave differently cannot share a
type: one type cannot be both a gate and an inert annotation, and the
combinations that contradict each other are rejected by name when you
try. If the two edges you are about to merge would want different
traits, the decision has already been made for you.

`semantic_role` and `analysis_traits` are two separate declarations and
neither implies the other: the role is what a view reads to know what an
edge *means*, the traits are what a walk reads to know what it *does*.
Declare both at the moment you declare the type. Six months on,
re-deriving what thirty relation types meant from their names is a job
nobody does correctly, and every reader after you inherits the guess.

## Decision 4 — field, `longtext`, or document?

If a query, a view or an analysis would ever need to read **inside** it,
it does not belong in a document. `longtext` is prose short enough to
sit in a table cell — a one-line objective, a flavour sentence. A
dialogue script, a chapter of lore, the game bible: those are documents,
attached to the entity they belong to.

The cost of putting a novel in a `longtext` field is paid by everyone
who lists that type afterwards. The cost of putting a queryable value in
a document is that it is invisible to every view in the game.
`reference/documents.md` has the sequence.

## When you are not sure

Prefer the relation. Turning a relation into a field later is one read
and one write per row and loses nothing but the edges. Turning a field
into a relation later is the six-hundred-call repair at the top of this
page.
