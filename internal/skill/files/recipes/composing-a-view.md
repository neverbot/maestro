# Composing a view

A view is a stored query plus the renderer that draws it. Composing one
is a loop, and this page is about the loop — the shape of a query
document, its operators and its renderers are in the descriptions of the
tools that take it.

## The loop

1. **Start from the nearest worked example, not from an empty
   document.** `genres/mmorpg.md`, `genres/racing.md` and
   `genres/metroidvania.md` each end with a view over a real game, and
   `views.get` will hand you any view this game already has. A document
   that already resolves against a game is a far better starting point
   than a blank one, because most of what goes wrong is the join between
   the query and the vocabulary.
2. **`views.validate`.** It judges the document without saving it and
   without running it, so it costs a catalogue read rather than a graph
   walk.
3. **Read the pointer. Fix that one position. Validate again.** Two
   iterations is the expected number for a traversal against a game you
   have not written yourself. It is not a sign you are doing it wrong;
   it is what the cheap call is for.
4. **Then run it, or save it.**

That third step is the reason this loop is on a page at all. A tool
description can say what one call answers; it cannot tell you that
calling it three times is the normal case, because from inside one call
there is no second call.

## Run once, or save?

Save when a designer will open it again. A view is an artefact of the
game: it appears in `views.list`, somebody arranges its nodes, somebody
puts a map behind it.

Run it inline and save nothing when the question was asked once in
conversation. "Which quests can a Mage reach?" is a sentence in a chat,
not a thing the game contains: run the document with `views.run` and
store nothing. A game whose saved views are forty one-off questions has
no saved views at all.

If you saved one and it turns out nobody wanted it, `views.remove`
deletes it. Removing your own experiment is tidying; removing a view a
designer made is not yours to do.

## The failure that costs the most and raises no error

**A picture that is really a typo.** The document is valid, the call
succeeds, and the answer is a handful of nodes and no edges. Nothing
refused anything, so nothing tells you.

When a picture comes back nearly empty, suspect the query before you
suspect the game:

- **A traversal written the wrong way round.** Direction is taken
  literally and inferred from nothing. Walking `takes_place_in` outward
  from a zone finds nothing, because that edge runs quest → zone; the
  quests in a zone are the inward walk. Both documents are valid.
- **`matches` used as a regular expression.** It is a glob: `*` and `?`
  are the metacharacters and everything else matches itself, so a value
  like `.*forest` matches only a string that literally starts with a
  dot. Nothing is refused and nothing matches.
- **A step that filtered everything out.** A `to_type` naming a type
  that is never at the far end of that edge, or a condition on a field
  whose values are spelled differently from your literal — keys are
  matched without regard to case but values are not.
- **An edge spec joining two sets that never touch.** Nodes appear,
  edges do not, and the picture looks like a game with no relations.

The way to tell a typo from an empty answer is to take the query apart:
run the selector alone, then add one step at a time. Four cheap runs
find it; staring at the document does not. `relations.get` on one edge
you expected to see will tell you in one call whether the edge is
missing or the walk is.

## What belongs to the designer

Some of this surface is not for you.

- `views.set_positions` and `views.clear_positions` hold where each node
  sits in one view. They are a designer's afternoon of map work.
  Positions survive an edit to the query and survive the game growing,
  so composing a better query does not disturb them — clearing them does.
- `views.set_background` puts an image behind a view, and
  `views.list_assets` is where the id of an already-uploaded image comes
  from. Neither uploads anything: images arrive from a browser.

The rule is the same as everywhere else in this bundle. Content and
queries are what you are here for; the arrangement of a picture is
somebody else's judgement, and it is not recoverable from a backup you
did not take.

## Where to look next

- `reference/queries.md` — why the query language is worth learning,
  and in what order to build a document.
- `genres/metroidvania.md` — a map view over a game whose edges carry
  the interesting values.
- `reference/errors.md` — the refusals a query can produce, and what
  each one asks you to change.
