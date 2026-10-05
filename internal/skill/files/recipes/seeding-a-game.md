# Seeding a game

Filling an empty game, or adding a whole subsystem to one that already
has content. Every step here spans at least two calls; anything that
fits inside one call is in that call's own description and is not
repeated.

## The order

1. **`types.upsert`** — every entity type, with its field schema.
2. **`relation_types.upsert`** — every relation type, with the entity
   type keys admitted at each end, its meaning and its behaviour.
3. **`entities.upsert`** — the rows, in batches, one type at a time.
4. **`relations.upsert`** — the edges, in batches, once both endpoints
   exist.
5. **`games.counts`** — read the shape back.

The order is not a style. Each step is what the next one is checked
against: an edge type names entity types that have to be declared
already, and an edge names two rows that have to exist already. A seed
written in any other order fails on its own dependencies, one call at a
time, and the failures look like the surface being difficult rather than
the plan being backwards.

**Do the whole vocabulary before any content.** Declaring three types,
seeding them, then declaring the fourth is a working sequence that costs
you the one moment when the shape is cheap to change. Types and relation
types together are a dozen calls; look at them as a set before writing
five hundred rows against them.

## Batching

Split by the bound the batch tools name in their own descriptions, count
before you send, and keep one type per call while you are seeding —
mixing types in a batch is admitted and makes a failure harder to read.

The property that matters across calls: **a batch is refused as a
batch.** An oversized batch does not write a prefix, so a failure means
you resend the batch — not that you go looking for which rows landed.
Within an accepted batch the default mode is per-row, and the answer
names what landed and what did not, so keep the answer: it carries the
new version of every row you just wrote, and that is what the next
update needs.

**Seed edges last, and per relation type.** An edge naming a row that is
not there yet is a failure you caused by ordering, and it is
indistinguishable in the answer from an edge naming a row you misspelled.

## Re-running a seed

Re-running is not free today, and pretending otherwise is how an agent
loses an afternoon.

Every write is a compare-and-set. A row that already exists is updated
only against the version you claim for it, so a second run of the same
payload conflicts on every row that is already there. **That is the
versioning working**, not a bug: two agents seeding the same game from
two stale copies is exactly what it exists to stop.

Until the surface grows a conflict mode, a re-seed is three steps:

1. `entities.list` the type. The slim listing carries each row's key and
   its current version, which is all a re-seed needs — you are about to
   send every field value again anyway, so do not ask for the fields.
   **This is the only case where the slim listing is enough**; see below
   for the one that looks like it and is not.
2. Send the same items back, each with the `expected_version` the
   listing gave for its key.
3. For a key the listing did not return, send it with no version claim
   at all. It is new, and claiming a version for a row that is not there
   is a different answer entirely.

**Do not carry versions across a session boundary.** Read them again.
The listing is one call per type and you are about to make hundreds; a
version from an hour ago is a guess about what a designer did in the
meantime.

The same three steps work for edges, with `relations.list` and
`relations.upsert` in place of the entity pair.

**A re-seed is not a partial update, and the difference is destructive.**
A re-seed hands back every field it ever wrote, so replacing the row's
whole map costs nothing. Changing one field and keeping the rest is a
different call: by default a write replaces the whole map, so a field you
leave out is cleared rather than left alone. Two ways out, and the first
is the one to reach for: send `fields_mode: "merge"` and name only the
fields you are changing, or read the row first — `entities.get`, or the
listing with `verbose` — change the value inside the map you read, and
send that map whole. `reference/fields.md` states the rule, and the
tool's own description states it for the call.

## Reading the shape back

`games.counts` in one call: every declared type, how many rows instance
it, how many are flagged invalid, and the totals. Compare it with what
you meant to write before you tell anyone you are done.

Two numbers are worth looking at:

- **A declared type with a count of zero.** Either the batch for it
  failed and you did not read the answer, or you declared vocabulary the
  game does not use. Both are worth saying out loud.
- **A non-zero invalid count in a game you just seeded.** Rows you wrote
  that do not fit the schema you wrote. That is your own edit, and it is
  the one case where repairing without asking is right — see
  `reference/errors.md` for which repair.

For one row, `entities.get` and `relations.get` read it back by the
address you wrote it under, which is the fastest way to check that a
field you thought you set actually landed.

**Read it back after step 4, not during.** A game between steps 3 and 4
is *supposed* to look like a field of disconnected rows: the edges are
step 4 and nothing before it can show them. Reading that as a broken
design and starting to repair it lands your repairs on top of the seed
that was about to write the edges.

## Undoing

Removals run in the reverse of the order the seed was written:
`relations.remove`, then `entities.remove`, then
`relation_types.remove`, then `types.remove`. The last two on
something that still holds content answer `in_use` rather than taking
the content with them, which is the one
place this surface refuses to let a teardown quietly delete a game's
content. Read the shape once before a teardown and once after: a count
that did not move is a teardown that removed nothing.

## What this recipe will look like when the surface changes

This page teaches a read-then-write loop **because the batch tools take
no conflict mode today.** A test in this repository fails the day one
arrives, and this page is rewritten around it in that same change. If
you are reading this and the descriptions do offer one, this page is out
of date and the description is right.

## Where to look next

- `genres/mmorpg.md` — a whole seed, with the modelling decisions named.
- `modelling/deciding.md` — before step 1, if the shape is not settled.
- `reference/errors.md` — what each refusal means and which recovery it
  asks for.
