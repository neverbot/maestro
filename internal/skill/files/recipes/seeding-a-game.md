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
   its current version, which is all this needs — do not ask for fields
   you are about to overwrite.
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
