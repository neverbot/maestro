# Mistakes that cost a rewrite

Every entry here is a shape that validates, seeds and lists perfectly.
None of them is caught by an error. Each is paired with what the repair
costs once the game has content in it, because the cost is the whole
argument.

## 1. A reference stored as text

**Before.** The `quest` type declares a `zone` field of type `text`, holding a zone's key. 300
quests seeded, every write green.

**The day it bites.** Somebody asks for the quest map. A traversal
cannot follow a text field, and a deleted zone leaves 40 quests pointing
at nothing with no error anywhere.

**The repair.** Declare the type, seed the zones, declare the relation
type, read 300 rows, write 300 edges, then remove the field — which
rewrites all 300 rows again. Six calls become six hundred.

**The rule.** `modelling/deciding.md`, decision 1: if the game has, or
could have, a row for the thing the value names, it is a relation.

## 2. Two vocabularies in one relation type

**Before.** One `requires` type carrying `kind: enum[hard,
recommended]`, because it seemed tidier than two.

**The day it bites.** Every question about what actually gates content
has to filter the enum out by hand, in every query, forever. The first
author who forgets ships a progression diagram with the recommendations
drawn as prerequisites, and it looks correct.

**The repair.** Declare the second relation type, read every edge of the
first, write half of them again under the new type, `relations.remove`
the originals, and fix every saved view that walked the old one. One
write per edge, plus a save per view.

## 3. A key derived from a display name

**Before.** The quest titled "The Dark Portal, Part 1" gets the key
`the_dark_portal_part_1`.

**The day it bites.** Week three, a writer renames it. An entity's key
is permanent, so the key now names a title nobody uses. Multiply by four
hundred rows and the game's addresses are a fossil record of a draft.

**The repair.** There is none that is cheap: a row's key cannot be
changed, so the only route is a new row, one write per edge and per
attachment to move what pointed at it, and `entities.remove` for the row
you are abandoning — whose history goes with it. Derive keys from what
will not change.

## 4. A level stored as text

**Before.** `min_level: "22"`, because it came out of a spreadsheet
that way.

**The day it bites.** "Quests between 20 and 30" is not askable: the
range operators are number operators, and a text field answers none of
them. A layered view cannot rank by it and a timeline cannot use it as
an axis. Sorting gives you 1, 10, 100, 2.

**The repair.** Change the field's declared type, which flags every row
that carries the old value, then repair all of them. One schema change
plus one pass per hundred rows — cheap by the standards of this page,
and still a day you did not need to spend.

## 5. Reading a half-seeded game as a finished one

**Before.** Types declared, 400 entities written, relations not yet.
You read the shape back and it is a field of disconnected rows.

**The day it bites.** Immediately, and it is self-inflicted. A game
mid-seed is *supposed* to look disconnected: the edges are step 4 of the
seeding loop and nothing before step 4 can show them. An agent that
reads that as a broken design starts repairing something that was never
wrong, and the repairs land on top of the seed that was about to write
the edges.

**The repair.** Finish the seed, then read the shape. Counting a game is
one call and it costs nothing to do it again afterwards; acting on it
early is what costs.

## 6. Fixing one problem per round trip

**Before.** A 200-row batch comes back with failures. You fix the first
one and send the batch again.

**The day it bites.** The same afternoon. Every refusal on this surface
reports every problem it can see at once, so the answer you already have
in your hands lists all of them, each at its own path.

**The repair.** Read the whole failure list, fix all of it, send once.
The difference is 200 round trips against one.

## Undoing is the seeding loop backwards

Every repair above ends in a removal, and removals run in the reverse of
the order the seed was written: edges, then rows, then relation types,
then entity types. `types.remove` and `relation_types.remove` on
something that still holds content answer `in_use` rather than taking
the content with it, which is a refusal doing you a favour — it is the
one place the surface will not let a teardown quietly delete a game's
content.

Read the game's shape once before a teardown and once after. A count
that did not move is a teardown that removed nothing.
