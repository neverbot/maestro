# Comments

A game's log, and **the one place you and the game's designers write to
each other**. A comment is markdown about the work rather than about the
game: how a thing was imported, what was rewritten and why, an idea about
the philosophy of a type, something worth doing later.

Three tools, and `reference/tools.md` routes to their contracts:
`comments.add`, `comments.list`, `comments.remove`.

## Somebody reads these

**Every comment is drawn on the thing's own page**, under its content, in
a band a designer sees whenever they open it: newest first, each with who
wrote it and how long ago, the markdown rendered. A designer writes there
too, in the same band, and you will read what they wrote the next time
you call `comments.list`.

So write for that reader and not for yourself. "Imported from the old
engine's data files; the damage formula is a guess from two log lines" is
worth opening a page for. "Updated" is a line somebody has to decide what
to do with.

## What belongs here, and what does not

> If a player could meet it, it is a field or a document. If the next
> person to open this thing would want to be told it, it is a comment.

A field holds the game: a quest's objective, a circuit's length, the
level something is granted at. A document holds the game's long prose: a
dialogue script, a chapter of lore. **Neither is the place for "imported
this from the old engine, the damage formula is a guess"**, and that
sentence is exactly what a comment is for.

**No query selects on a comment, no view draws one, no analysis counts
one.** That is what lets the log hold anything you can write: nothing
downstream depends on its shape, and the only thing that reads it is
somebody who came to find out what happened here.

It is also why the division runs the way it does. **The model is the
design as it stands and the log is how it got there**: a thing's fields
say what it is and how it works today, and what it used to be, what
changed, why, and what might come next are comments.
`modelling/deciding.md` argues it, and says why the convention is one to
propose to a game's designers rather than to apply.

## The three things that carry one

An entity, an entity type and a relation type: **the three that have a
page**. Each is addressed the way the rest of this surface addresses it —
by keys, never by ids — and the target's own `on` says which. The tool's
description carries the three spellings and the arguments each takes.

**One relation carries none, deliberately.** An edge has no page, so a
note left on one would be reachable by `comments.list` and by nothing a
designer opens: a place to write where nothing reads. What was going
there belongs in one of two places instead.

- **A fact a player could meet is a field on the edge.** The level a
  connection grants something at, the lap count a championship entry is
  worth: in a field a `where` reads it, a view draws it and
  `relations.repair` can rewrite it across the type. In a comment it is
  out of reach of all three, forever. If the fact is per-something and
  the field is single valued, that is a modelling decision to make, not a
  reason to put game content in the log.
- **A note about how a kind of connection changed goes on the relation
  type**, which has a page, or on one of the entities the edge joins.

**A document carries none** either: it already keeps a message per
version, which is the same note in the place that can say which change it
was about.

An address this game does not have is `not_found`. A comment against a
row nobody can reach is a note the log fills with and nothing ever reads,
so it is refused rather than stored.

**A document carries none.** It already keeps a message per version,
which is the same note in the one place that can say which change it was
about.

## What a log is not

**There is no state on a comment and there will not be one.** No status,
no assignee, no due date, no "done". Maestro is not a project tracker,
and a log that grows a state has become one. Write "worth checking
whether the two versions of this spell should be one row" and leave it
written; nothing will ever mark it done, and that is the arrangement.

**There is no edit.** A log that can be rewritten is not a log: a comment
is what somebody said at a moment. Taking one out is the only change a
log admits, and the tool that does it says what that costs.

**Removing the thing removes its log.** The comments on an entity go when
the entity does. There is no orphan to find later and nothing to clean
up.

## How to use it

- **Arriving at a game you have not touched in a while**: `comments.list`
  with no target at all is the whole game's log, newest first — yours and
  the designers'. It is the fastest answer to "what happened here since I
  was last in", and the designers' notes are where you learn what they
  want that nothing in the model says.
- **Writing one while you work**: short, one subject, and say the thing
  rather than the category.
- **Length**: a comment holds at most 4000 characters, and the bound is
  there to make a decision rather than to save bytes. Prose that wants a
  title, a history and an address of its own is a document.
