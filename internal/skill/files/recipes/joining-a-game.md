# Joining a game somebody else designed

You have a token and a game you have never seen. The goal of this recipe
is to know the game's shape before you write anything, in three calls
rather than ten.

## 1. Where you are

`whoami` first: who you act for, and the slug of the single game your
token is bound to. `games.get` and `games.list` both answer about that
same one game — the binding is the token's, so neither is a way to look
around, and neither adds anything to what `whoami` already said.

## 2. The shape, in one call

`games.counts` answers the whole question at once: a row per declared
entity type and per declared relation type, each with how many rows
instance it and how many of those are flagged invalid, plus the totals.

Read it before anything else, because it tells you where the game
actually is. A game with forty declared types and content in six is a
game with six types that matter and thirty-four somebody declared in a
planning session. Spend your reading on the six.

## 3. The vocabulary, then a sample

- `types.list` and `relation_types.list` — the vocabulary, slim: key,
  label, version. Cheap enough to read whole.
- `types.get` and `relation_types.get` — one type in full, for the two
  or three the counts say are populated. This is where a field schema
  and a relation type's admitted endpoints come from, and you need those
  before your first write, not before your first read.
- `entities.list` with fields, on those same two or three types — a page
  is enough. `entities.get` reads one row in full when a listing raises
  a question about it.

**What you are looking for in the sample is convention, not data.** Are
keys `snake_case` or `kebab-case`? Is the language English? Does a
`summary` field hold one line or three paragraphs? Are enum options
lower-cased? None of that is enforced by the surface and all of it is
obvious to a designer reading what you wrote next to what they wrote.

`search` finds a name across entities and documents when you know what a
thing is called and not what type it is. `views.list` shows what
questions the designers already ask of this game, and a saved view's
query is often the clearest statement of how they think about it.

## The judgement: a non-zero invalid count is information, not a task

Rows a schema edit stopped fitting are kept and flagged, deliberately —
nothing was deleted and nothing was filled in with a guess. A game with
eighty invalid rows is a game where somebody added a required field last
week and has not finished the migration.

**Report it. Do not repair it because you noticed.** You do not know
whether the field is settled, whether the values are coming from a
spreadsheet, or whether the whole type is about to be renamed. Repairing
somebody else's flagged rows destroys the only record of what still
needs a decision, and the flag is that record.

The exception is the game you seeded yourself, ten minutes ago, in this
session. That is `recipes/seeding-a-game.md`.

## What not to do on the way in

- **Do not page the whole game.** A catalogue walk of every type is
  hundreds of calls to learn what one call already answered.
- **Do not re-read what you loaded.** The game did not change under you
  unless you changed it.
- **Do not declare anything yet.** A type added by an agent on its way
  in is the fastest way to make a designer distrust the whole session.
  Propose it, in words, and let them say yes.
- **Do not assume a name means what it means in your last game.** A
  `zone` here may be a UI region, a legal territory or a difficulty
  band. `types.get` and the sample rows are how you find out.

## Where to look next

- `genres/racing.md` — a worked game, if the vocabulary you found looks
  like a career mode.
- `modelling/mistakes.md` — recognising a shape that is going to cost
  somebody a rewrite, before you build on it.
- `recipes/composing-a-view.md` — answering a question about the game
  you just read.
