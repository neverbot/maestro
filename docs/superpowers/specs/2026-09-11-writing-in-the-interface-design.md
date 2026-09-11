# Writing in the interface: what happens when the world moved

**Date:** 2026-09-11
**Task:** Writing 1 (`565783e5`), under "Content is read in the interface
and written only by agents" (`87e466ab`).
**Decides:** the conflict answer, once, before any form exists. Also what
a person may write at all, and how a read-only product says so.

Every write in this product is a compare-and-set: the caller states the
version it read, and a stale claim is refused with `409 version_conflict`
carrying `current_version`. That design is right and it is not changing.
It is also a conversation a person should never have to hear, so what a
form does with that refusal is the whole of this document.

## 1. The principle: the edit is never lost, and never silently wins

Two failures bracket this, and both are worse than a visible refusal.

**Losing the edit.** A form that refuses, clears itself and says "please
try again" has thrown away the only thing in the exchange that did not
exist anywhere else: what the person typed. Their sentence is not
recoverable from the server, from the other writer, or from anywhere.

**Silently winning.** A form that catches the refusal, re-reads the
current version and writes again with the new one has implemented
last-writer-wins with extra steps, and neither writer is told. That is
the worse of the two, because nothing looks wrong.

So: **the interface never retries a compare-and-set.** Not once, not with
a backoff, not "only when the field we changed is not the field they
changed". A refusal is shown, the edit stays in the person's hands, and
the next move is theirs.

## 2. What a refusal looks like

The server hands back the current version and the domain's own sentence.
The screen turns that into three things, in this order, in place — never
a modal, and never a banner at the top of a page whose edit is at the
bottom:

1. **What happened, in the reader's words.** *"Somebody changed this
   while you were editing."* Not "version conflict", not "409", not the
   version number: the number is a fact about the protocol and means
   nothing to the person holding a better name for a quest.
2. **What it says now**, read from the server, shown beside what they
   typed. Two values, labelled *theirs* and *yours*. This is the whole
   decision, and it cannot be made without seeing both.
3. **Two actions and no default.** *Keep mine* re-writes on top of the
   version just read. *Take theirs* discards the edit and shows the
   current value. There is no third button and neither is primary: the
   product does not have an opinion about whose text is better, and a
   pre-selected answer to that question is the product having one.

`Keep mine` is the only place in the product that writes with a version
it did not read from a page a person looked at — so it reads, shows,
and writes in one gesture the person authorised, which is the difference
between a retry and a decision.

**When the two values are equal**, the refusal is not shown at all: the
other writer wrote the same thing, the person's intent already holds, and
telling them about a conflict with no consequence is noise. The write is
simply reported as done.

## 3. The event stream gets there first

The product already streams changes over SSE, and a form that ignores
them will refuse at save time for a reason it knew about a minute
earlier.

So an edit in flight subscribes to its own row. When the stream says that
row changed while a field is open:

- **The field is not touched.** Nobody's typing is interrupted, and a
  value that changes under a cursor is the worst thing this could do.
- **A line appears under it**: *"This changed a moment ago. What it says
  now: …"*, with the same two actions as §2.
- If the person saves anyway, they get §2's exchange, which they have
  already seen — so the refusal at save time is a confirmation rather
  than a surprise.

This is the same shape twice on purpose. One conflict answer, arriving
either from a notification or from a refusal.

## 4. What a person may write

**Yes, in this order of obviousness:**

- An entity's **name**. The typo a designer is looking at.
- An entity's **field values**, against the schema its type declares.
- A document's **body** — already versioned, already diffed, and the one
  place the product's own history makes a conflict legible.

**No, and the reason is not timidity:**

- **A type's field schema.** Narrowing a schema flags every row that no
  longer fits, and the repair for that is a bulk operation an agent runs.
  A form that let a person remove a field would let them mark four
  hundred rows invalid with one click and offer them no way back. If it
  is ever built, the design owes a **preview**: *"237 of 412 quests would
  stop fitting"*, read before the write, from the same count the repair
  operation uses. Without that preview it is not a form, it is a trap.
- **Relations.** An edge has two endpoints and its own fields; creating
  one from a form means an entity picker, a relation-type picker and a
  field form, which is most of the query builder's furniture. It waits
  for those pickers, which are already owed.
- **Anything cascading.** Renaming a *type's key*, deleting anything.
  Those have repair operations for a reason.

**The rule underneath:** a person may write what they can see the whole of
on the screen they are on. A write whose consequences are elsewhere needs
the elsewhere shown first, and that is a different design each time.

## 5. Until then, the product says it is read-only

This is the part that is owed today, before any form exists, and it is
the smallest item in this document.

The product is read-only and **says so nowhere.** `design.md` specifies a
Read-Only Notice; it appears on no screen. Two separate `impeccable`
critiques found it, the second exactly: *the role sentence only appears
inside empty states, so the fuller the game, the less the page admits what
it cannot do.* That inverts principle 5, which is that what the product
cannot do is written where somebody would look for it.

So, before Writing 2 and independent of it:

- **Every screen showing content a person cannot change carries the
  notice**, in the page head, where the action would be: *"Read-only,
  written by the agent"*, dashed outline, label type, muted.
- **It is role-aware**, the way the empty states already are: a viewer is
  told the instance will refuse a write from them, an editor is told
  writing goes through an agent. The product already has `whoWrites()`
  for exactly this sentence and it is only wired into empty states.
- **It disappears field by field as writes land.** The notice is a claim
  about the screen, so the screen that gains a rename loses the part of
  the claim that said names cannot be changed. A notice that outlives the
  limitation it describes is the next "prose about code" defect.

## 6. What this does not decide

- **Optimistic display.** Whether a saved value appears immediately and
  is rolled back on refusal, or waits for the server. It is a question
  about latency on a local instance, and it should be measured rather
  than guessed.
- **Undo.** The document domain has versions and a revert; entities do
  not. Whether a rename is undoable is a domain question, not an
  interface one.
- **Who may write.** Roles already decide this server-side and the
  interface reads the answer; nothing here changes it.
