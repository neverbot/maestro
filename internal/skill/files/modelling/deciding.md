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

Splitting one type into two after the fact costs a write per edge —
read them, rewrite half under the new type, remove the originals — plus
one save for every stored view that walked the old one.

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
it is a field. If its history is the point — you will want to know what
it said last month — or it is a thing somebody would go and find on its
own, it is a document. Both hold markdown, so this is never a decision
about formatting, and **it is not a decision about length**: ten lines of
notes with a list in them are a field.

A dialogue script, a chapter of lore, the game bible: documents, attached
to the entity they belong to. An objective, a flavour paragraph, a note
about how this rule behaves: fields.

The cost of putting a queryable value in a document is that it is
invisible to every view in the game. The cost of putting a document in a
field is that there is no history: a write replaces the value and what
was there is gone. `reference/documents.md` has both lists.

## Decision 5 — the model is the present tense

**Maestro holds the design as it stands.** An entity's fields say what
the thing *is* and how it *works*, now: what a player meets if they meet
it today. Everything about how it got there — what it used to be, what
changed and why, the thinking behind a decision, an idea for later — is
the log, and `comments.add` is where it goes.

The split pays for itself in both directions. A field that accumulates
"was X until the rewrite, then Y, now Z" stops answering the question a
view asks it, and the row's own page stops being readable as a statement
of the game. A log that holds the current design instead is worse: no
query selects on a comment, no view draws one, no analysis counts one, so
a rule written there is a rule the game does not have.

A habit that follows from it, **and that the designers have to agree to
before you adopt it**:

> One own field — `notes` is the usual name — carrying the design
> decisions that are true now and that no other field holds: how this
> thing behaves, what it is for, the constraint a reader has to know.
> And a comment for each change to that: what it was before, what moved
> and why.

This is useful often and it is not a law, because it is a decision about
*their* game's schema and not about the metamodel. Propose it, say what
it buys, and work to the answer you get: a game may want the design
spread across several named fields rather than one `notes`, may want a
document instead because the prose is long and its history is the point,
or may want neither. What does not change with the answer is the
direction: the current state in the model, the record of change in the
log.

Two consequences worth stating, because both have been got wrong:

- **When what a thing is changes, update the row and write a comment.**
  Not a second row, and not a `history` field growing inside the first.
  `modelling/naming.md` argues the one-row half.
- **A document is not the log either.** It keeps a version per write with
  a message, which is the history *of that prose*; it is not where the
  philosophy of a change to the model belongs.

## When you are not sure

Prefer the relation. Turning a relation into a field later is one read
and one write per row and loses nothing but the edges. Turning a field
into a relation later is the six-hundred-call repair at the top of this
page.
