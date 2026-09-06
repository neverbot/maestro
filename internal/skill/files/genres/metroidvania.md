# A worked example: a metroidvania

A single connected map, abilities that open parts of it, bosses that
drop those abilities, and items scattered through rooms. The transcript
beside this page — `genres/metroidvania.json` — is the example as an
ordered list of calls, applied to a real game by a test on every build.

This genre is in the bundle for one reason: it is the clearest case
where **the interesting data is on the edge and nowhere else**.

The entity types it declares:

```vocab:genre_types_metroidvania
room ability boss item
```

And three relation types: `connects_to`, `located_in`, `drops`.

## Four types, and why there are not more

A door is not an entity here. Neither is a region, a save point, or a
gate. Every one of those was considered and every one is a field or an
edge:

- A **region** is an `enum` field on the room. It groups rooms for
  colouring a map; nothing ever points at a region, so it has no rows.
- A **save point** is a `bool` on the room, for the same reason.
- A **door** is an edge of `connects_to`. There is one per pair of
  rooms, it has its own properties, and it is exactly what an edge is.

The rule that produced this list: **something is an entity when other
things point at it.** Abilities are pointed at — by bosses that drop
them and by doors that need them — so `ability` is a type with rows.
Regions are pointed at by nothing.

## The edge is the door, and it carries the gate

`connects_to` runs room → room and carries two fields:

- `passage`: a door, a shaft, a gate, a breakable wall.
- `requires_ability`: the ability the player holds before crossing.

Neither belongs on a room. The Flooded Stair is reached from Drip Hall
through a gate needing the Rebreather and leads on to the Sunken Vault
through a shaft needing the Double Jump; a field on the room would have
to hold both and could hold one. That is law 2, and this is the case it
was written for.

## The honest limit: `requires_ability` is a soft reference

`requires_ability` is a `text` field holding an ability's key, and
**nothing checks it**. Misspell `double_jump` and the game stores the
misspelling. Rename the ability and the doors go on naming the old key.
There is no reference field type in Maestro and there will not be one.

This looks like a violation of law 1, and the distinction matters:

- Law 1 is about **a row referring to another row**. A quest's zone is
  an edge, not a field, and there is no exception.
- Here the *source* of the reference is an edge, and an edge already
  joins two rooms. An edge cannot also have an endpoint at an ability;
  that is a hyperedge and this model has none.

So there are two honest ways to write this gate, and the transcript
takes the cheaper one deliberately:

1. **A text field on the edge**, as here. Cheap, readable, and
   unvalidated — the game keeps it consistent itself, and a rename of an
   ability means a sweep over the doors.
2. **The gate as its own entity.** Declare a `gate` type, point the
   rooms at it and point it at the ability, and every reference is an
   edge again. Correct, traversable, and it turns twelve doors into
   twelve extra rows plus twenty-four extra edges.

Take the second when the gate has to be *walked* — when you need "which
rooms open up if the player finds the Dash". Take the first when the
value is read by a human looking at a map. A game that starts with the
first and needs the second later pays one write per edge, which is why
this is a decision worth taking on purpose rather than by default.

## `connects_to` declares no behaviour

The transcript gives `connects_to` a meaning — spatial — and leaves its
analytical behaviour undeclared. That is not an oversight.

A shaft the player falls down and cannot climb back up is not symmetric.
A door needing an ability is not a prerequisite edge in the sense the
vocabulary means, because the prerequisite is a value on the edge and
not the edge's other end. No word in the vocabulary is true of this edge
type, so no word is declared. A wrong behaviour is worse than none: a
graph walk acts on it.

## `drops` and `located_in` are two edges, not one

A boss is *in* a room and *drops* an ability. Both could be spelled as
one "boss to thing" edge type with a field saying which, and that would
be the mistake this page exists to name.

They are different in three ways at once: the entity types at the far
end differ, what they mean differs — one is containment, one is a reward
— and they are walked in different questions. "What is in this room"
walks one; "where does the Dash come from" walks the other. An edge type
whose meaning depends on reading one of its fields is a type doing two
jobs.

## Where this differs deliberately

**Against `genres/mmorpg.md`, on where a gate lives.** That game gates a
quest with an edge to a class row: `available_to` joins two things the
game has rows for. This game gates a crossing with a field on that
crossing, because a door's condition belongs to that one door and to
nothing else.

The question that separates them is: **is the gate a property of the
connection, or a connection in its own right?** A class is a row that
hundreds of quests share, so it is a row. A door's required ability
varies per door, so it is a field — and it comes with the soft-reference
cost named above, which the MMORPG's approach does not have.

**Against `genres/racing.md`, on how many relation types progression
needs.** That game splits its progression across `requires` and
`unlocks`, one for each direction of the same gate, because its gates
are objects the player collects. Here progression is one relation type,
`connects_to`, plus the values on it: the map *is* the progression, and
an ability is only interesting where a door mentions it.

## Where to look next

- `modelling/deciding.md` — the field-or-relation decision, and the
  cost of each direction.
- `modelling/mistakes.md` — the shapes that cost a rewrite, including
  the one this page took on purpose.
- `reference/fields.md` — what a `text` field is and is not.
