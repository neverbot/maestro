---
name: maestro
description: Use when designing the content of a video game in Maestro — declaring a game's own entity and relation vocabulary, seeding characters, places, missions and progression, composing saved views over them, or joining a game somebody else designed.
---

# Maestro skill

Maestro records the **content design** of a game: what a player can be,
where they can go, what they can do and what they unlock. It is not a
task tracker and there is no kanban here.

Maestro ships **no built-in game vocabulary**. There is no Quest, no
Zone, no Class, no Circuit. A game declares its own, and that is the
product rather than a gap. You will declare a vocabulary before you can
write anything, and the shape you choose is the thing that is expensive
to change later.

Every tool's full contract is in that tool's own description, which your
client already holds. These pages teach what no single description can:
the order, the judgement, and the consequence that only shows up two
calls later.

## 1. Identify yourself

Call `whoami` first. It says who you act for and which single game your
token is bound to, and the slug it answers with is that game's address —
the value the optional `game` argument on every other tool is checked
against.

One token, one game. A `scope_violation` is the answer to a request that
names a different game, not a bug to route around. Ask the human for the
right token.

## 2. The four primitives

| Primitive | What it is | Declared with |
|---|---|---|
| **EntityType** | a kind of thing this game contains | `types.upsert` |
| **Entity** | one instance of a kind | `entities.upsert` |
| **RelationType** | a kind of directed edge between things | `relation_types.upsert` |
| **Relation** | one edge between two entities | `relations.upsert` |

Everything else in Maestro — catalogues, place graphs, mission lists with
their preconditions, progression trees — is a **view** over those four.
There is no fifth primitive and no built-in notion of a quest.

Every field an entity or an edge carries is one of these types, and there
are exactly six:

```vocab:field_types
text longtext number bool enum list<text>
```

There is no reference type. That is law 1.

## 3. The four laws

Stated flat here because you need them before your first entity type
exists. Argued, with what each costs when broken, in
`modelling/deciding.md`.

1. **A reference to another entity is a relation. Always.** A quest's
   zone is not a `text` field holding a zone's key. It is an edge. The
   day someone wants the map, a text field cannot be traversed and the
   repair is one write per row.
2. **Data that belongs to the connection goes on the connection.** Edges
   carry their own declared fields. A door's required ability belongs on
   the edge, not on either room.
3. **Declare the direction when you declare the relation type, and name
   the type so it reads source → target.** `takes_place_in`,
   `available_to`, `unlocks`, `connects_to`. Queries take direction
   literally and infer nothing.
4. **Say what an edge means, and how it behaves, at the moment you
   declare it.** Two separate declarations on a relation type, and
   declaring one of them is not declaring the other: `semantic_role` is
   the meaning a view reads, `analysis_traits` is the behaviour a graph
   walk reads. Six months on, re-deriving what thirty relation types
   meant is a job nobody does correctly.

The meanings, chosen before any description is any use to you:

```vocab:semantic_roles
prerequisite unlock containment spatial availability reward
```

The behaviours are a different, longer list, enumerated in
`relation_types.upsert`'s own description together with the
combinations it rules out. Read it once before your first edge type.

## 4. Keys are the API

- Everything is addressed by **key**; a game is addressed by **slug**.
- Keys are matched **without regard to case**. `Quest` and `quest` are
  one key, and writing the second when the first is stored is refused,
  naming both spellings, rather than quietly updating the row.
- **An entity's own key is permanent.** Choose it from something that
  will not change — not from a display name a writer will rewrite.
- **An entity type's key and a relation type's key can be changed**, with
  `types.rename` and `relation_types.rename`, addressed by the key each
  has now. Saved views that named the old key go on running and report
  what drifted, at the position in their own query document.
- Two spelling rules, not one, and they differ because the mechanisms
  differ: a **field** key is `^[a-z][a-z0-9_]*$`, a key that addresses a
  **row** is `^[A-Za-z0-9][A-Za-z0-9_-]*$`. Both capped at 64, both
  ASCII. `modelling/naming.md` has the reason and the rest of the rules.

The choice of language for a game's keys is **the game's**: a Spanish
studio's `mision` is as correct as `quest`. Both rules are ASCII, so
`misión` is no key at all — the case-folding index talking, not a
preference about language.

## 5. The seeding loop

In this order, because each step is what the next one is checked against:

1. `types.upsert` — every entity type, with its field schema.
2. `relation_types.upsert` — every relation type, with its endpoint type
   keys, its meaning and its behaviour. An edge type whose endpoint
   types are not declared yet cannot be declared.
3. `entities.upsert` — in batches, per type.
4. `relations.upsert` — in batches, once both endpoints exist.
5. `games.counts` — read the shape back and check it is what you meant.

**Every write is versioned.** Send `expected_version` on every update,
matching the version you read. A mismatch is `version_conflict` and
carries what is current: re-read, merge, retry. Do not retry blind.

**A version claim is a claim about a row that exists.** Claiming one for
a row this game does not have is `not_found`, and is never a quiet
re-creation.

**Batches are bounded, and a batch over the bound fails as a batch**
rather than silently writing a prefix. Split it yourself, before sending.

## 6. Response discipline

Listings are slim by design — id, key, label, version. Ask for fields
only when you will use them, and page rather than raising a limit. Do
not re-read a page you already loaded this session: the surface has not
changed under you unless you changed it.

## 7. Where to go next

| You are about to… | Read |
|---|---|
| decide a game's shape, or wonder whether something is a field or a relation | `modelling/deciding.md` |
| choose keys, or wonder what a rename costs | `modelling/naming.md` |
| recognise a shape that will cost a rewrite | `modelling/mistakes.md` |
| find which of the tools exists | `reference/tools.md` |
| declare a field type, or wonder what one costs later | `reference/fields.md` |
| read an error and pick a recovery | `reference/errors.md` |
| write a query or a saved view | `reference/queries.md` |
| attach prose to an entity | `reference/documents.md` |
| see a whole game modelled, in a genre near yours | `genres/mmorpg.md`, `genres/racing.md`, `genres/metroidvania.md` |
| fill an empty game, or re-run a seed | `recipes/seeding-a-game.md` |
| open a game somebody else designed | `recipes/joining-a-game.md` |
| turn a question about the game into a picture | `recipes/composing-a-view.md` |

Nothing in this bundle is required, and a game none of its examples
matches is the normal case.
