# Comments

A game's log. **A comment is markdown about the work, not about the
game**: how a thing was imported, what was rewritten and why, an idea
about the philosophy of a type, something worth doing later.

Three tools, and `reference/tools.md` routes to their contracts:
`comments.add`, `comments.list`, `comments.remove`.

## What belongs here, and what does not

> If a player could meet it, it is a field or a document. If it is what
> you were thinking while you wrote one, it is a comment.

A field holds the game: a quest's objective, a guild's vocation, the
level a spell is granted at. A document holds the game's long prose: a
dialogue script, a chapter of lore. **Neither is the place for "imported
this from the 1998 build, the damage formula is a guess"**, and that
sentence is exactly what a comment is for.

Nothing reads a comment but a person and you. No query selects on one,
no view draws one, no analysis counts one. That is the point: the log
can hold anything you can write, because nothing downstream depends on
its shape.

## The four things that carry one

An entity, a relation, an entity type and a relation type. Each is
addressed the way the rest of this surface addresses it — by keys, never
by ids — and the target's own `on` says which of the four it is. The
tool's description carries the four spellings and the arguments each one
takes.

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
  with no target at all is the whole game's log, newest first. It is the
  fastest answer to "what happened here since I was last in".
- **Writing one while you work**: short, one subject, and say the thing
  rather than the category. "Imported from the 1998 build; the damage
  formula is a guess from two log lines" is worth reading. "Updated" is
  not.
- **Length**: a comment holds at most 4000 characters, and the bound is
  there to make a decision rather than to save bytes. Prose that wants a
  title, a history and an address of its own is a document.
