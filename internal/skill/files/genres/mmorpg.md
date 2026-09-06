# A worked example: an MMORPG

A world with classes and races, a place graph, quest chains that gate
each other, talent trees, and reputation with factions. The transcript
beside this page — `genres/mmorpg.json` — is the whole example as an
ordered list of calls, and a test applies it to a real game on every
build, so nothing here is a decision that stopped working two months
ago.

Copying the vocabulary below and none of the reasoning is taking the
least valuable half. Your game's types will differ. The questions the
transcript answers are the part that carries over.

The entity types it declares:

```vocab:genre_types_mmorpg
class race profession zone dungeon quest talent faction
```

And five relation types: `connects_to`, `takes_place_in`, `requires`,
`available_to`, `rewards`.

## The order, and why it is the order

Types, then relation types, then entities in batches, then edges. Each
step is what the next one is checked against: an edge type names the
entity types allowed at each end, and an edge names two rows that have
to exist. `recipes/seeding-a-game.md` is that loop written out; this
page is about the eight choices made inside it.

## Decision 1: the talent tree — the rank is a field, the gate is an edge

A talent tree is the hardest thing in this game to model, because it
carries two different notions of "level" and they are not the same
notion at all.

- **The tier is a property of the talent.** A talent sits on row three
  of its tree whether or not anything precedes it, and a picture of the
  tree wants that number to lay the rows out. It is a `number` field on
  the entity: `tier`, with `max_ranks` beside it.
- **The gate is a fact about a pair.** "Impale is unavailable until
  Improved Heroic Strike is bought" is a statement about two talents. It
  is an edge of `requires`, and law 1 is not negotiable here: a
  `prerequisite_key` text field would look identical in a listing and
  would be untraversable the day someone asks which talents a build
  actually reaches.

The test for which one you are looking at: **could a second thing ever
be on the other end?** A tier cannot — three is three. A prerequisite
can, and in this transcript Mortal Strike has one and Combustion has
another. Everything that could have a second end is an edge.

## Decision 2: `available_to` points at two entity types

A quest can be restricted by class, by race, or by neither. Two edge
types — `available_to_class` and `available_to_race` — would double the
vocabulary to say one thing, and every query about "who can attempt
this" would have to walk both.

So one edge type, with `class` and `race` both admitted at the target
end. The endpoint declaration is the schema here: a quest pointed at a
`zone` through this edge is caught at write time rather than surviving
as a shape nobody expected.

The same reasoning puts `quest` and `talent` at the source end of
`requires`. A gate is a gate. Two vocabularies for one idea is two
queries for one question.

## Decision 3: what an edge means, and the one place nothing is declared

Every relation type in the transcript carries a `semantic_role`, and
four of the five carry `analysis_traits` as well:

| Edge type | Meaning | Behaviour |
|---|---|---|
| `connects_to` | spatial | symmetric |
| `takes_place_in` | containment | containment |
| `requires` | prerequisite | prerequisite_of |
| `rewards` | reward | unlocks |
| `available_to` | availability | *nothing declared* |

The last row is the interesting one, and it is deliberate. No word in
the behaviour vocabulary means "this edge says who may attempt the
thing". `annotation` is the word for an edge that is deliberately inert,
and `available_to` is not inert — it gates. Declaring `annotation` there
would be a false statement that a graph walk would then act on.

**An undeclared behaviour is honest; a wrong one is expensive.** The
meaning is declared either way, so a view still knows what the edge is
for. Read `relation_types.upsert`'s own description before your first
edge type: it carries the whole behaviour vocabulary and the
combinations it rules out.

## Decision 4: `connects_to` carries a field, and it is about the crossing

Zones connect by road, by boat and by portal. `travel` is a field on the
edge and not on either zone, because it is a property of the crossing —
the same two zones joined a second way would carry a second value, and a
field on the zone has nowhere to put it. That is law 2, and
`genres/metroidvania.md` pushes it much further.

`connects_to` also reaches a `dungeon`, while `takes_place_in` starts at
a quest. A dungeon is a place a zone leads to; a quest is a thing that
happens somewhere. Naming both edges "in" would have merged two
different questions into one unanswerable listing.

## Decision 5: reputation is a number on the edge

`rewards` joins a quest to a faction, and the standing granted is a
field on that edge. Two quests granting different standing with the same
faction is the normal case, and it is exactly the shape a field on
either endpoint cannot hold.

The same edge type also reaches `profession`, where the standing is
zero: the quest unlocks the profession rather than granting it a number.
One edge type, two things it can reward, one behaviour — `unlocks` —
that a walk can read for both.

## Where this differs deliberately

**Against `genres/metroidvania.md`, on where a gate lives.** This game
puts the gate on a relation to a class: `available_to` from a quest to a
class is an edge between two things the game has rows for. The
metroidvania puts its gate in a field on the edge — a door requires an
ability — because the *door* is the thing gated, not the room, and there
is a different door for every pair of rooms.

Both are correct. The question that separates them is: **is the gate a
property of the connection, or a connection in its own right?** Here the
class exists as a row, is queried on its own, and is one of a handful
shared by hundreds of quests, so it is a row and the gate is an edge to
it. There, the ability named by a door varies per door and belongs to
that crossing alone.

**Against `genres/racing.md`, on symmetry.** The place graph here
declares `connects_to` as symmetric: a road walked east is walked west.
The metroidvania's `connects_to` declares no behaviour at all, because a
shaft you can fall down and not climb is not symmetric. Same edge name,
same field-versus-edge law, opposite declaration — which is what "no
built-in vocabulary" means in practice.

## Where to look next

- `modelling/deciding.md` — the field-or-relation decision, argued
  rather than illustrated.
- `reference/queries.md` — the saved view this transcript ends with,
  and how to compose your own.
- `recipes/seeding-a-game.md` — the order above, as a workflow.
