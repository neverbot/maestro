# Queries and saved views

A view is a query document plus a renderer. This page holds the four
things the generated tables on the wire cannot say. It enumerates no
operators and no renderer parameters, deliberately: `views.run`,
`views.validate` and `views.upsert` each carry those tables in their own
descriptions, generated from the same Go the compiler reads, so a copy
here would be a copy that goes false quietly.

The renderers, which you pick between before any of that helps:

```vocab:renderers
graph layered nested map table timeline
```

## 1. The five stages are a shape to think in

A query document has five stages, and composing one in this order turns
a wall of JSON into five small decisions:

1. **`from`** — which entity types are in scope, and which rows of them.
   Everything downstream is judged against the types named here.
2. **`traverse`** — which relations to walk, from which set, in which
   direction. Direction is literal; nothing is inferred.
3. **`nodes` / `edges`** — what is actually drawn out of what the walk
   reached. Reaching a thing and drawing it are two decisions.
4. **`project`** — how each node presents itself: the slots a renderer
   reads, `color_by` and the rest.
5. **`limits` / `params`** — what bounds the walk, and what a run binds
   at call time.

Every refusal is addressed by a JSON pointer into the document, so a
failure names a position rather than the document. Fix that position.

## 2. Which failures are silent

This is the most valuable paragraph on the page and the reason it
exists. Three of these are quoted from the descriptions themselves,
because the wording is the contract and this page must not drift from
it.

> **From `views.run`'s own description:**
> **A misspelled type is refused by `@type eq`, `@type neq` and `@type
> in`, and draws nothing silently under `contains`, `starts_with` and
> `matches`.**

> **From `views.run`'s own description:**
> **An untyped `traverse` step switches off typo detection for the whole
> `project` stage, including the types the document did name.**

> **From `views.run`'s own description:**
> **A projected key and a compared key obey different rules, and they
> look identical in the document.**

What follows from all three, and what no single call can tell you: **an
empty answer is not evidence that the game is empty.** A run that draws
nothing has told you nothing about your query. When a picture comes back
empty, the first move is not to widen the query — it is to check that
the query says what you think, by naming a type under `eq` rather than
under a pattern, by giving every traverse step a `to_type`, and by
running the same selector with no `where` at all to see whether the rows
are there. Two calls, and you know which of the two possible worlds you
are in.

## 3. Composition is a loop, not a call

`views.validate` exists to be called more than once. Compose, validate,
read the pointer, fix that one position, validate again. **Two
iterations is the expected number**, not a sign that you are doing it
wrong — a traversal written against a game you have not seen will be
wrong about something, and finding out costs a catalogue read rather
than a graph walk.

Save or run once, at the end. `views.upsert` and `views.run` answer with
the same refusals at the same pointers, so nothing is learned by
discovering them the expensive way.

## 4. Staleness is the normal case

A view written before a type was renamed goes on running and comes back
whole. It reports what drifted, naming the old spelling and the new one
at the pointer in its own stored document that has to change. Nothing
rewrites a stored query behind its author, which is why the repair is a
save you make deliberately from the document you already hold.

A view naming a type that is *gone* is a different situation: that run
refuses, and the refusal carries the same pointers. Draw what is left if
you must, but read what was dropped — the parts that cannot run are
dropped whole, never run with a condition quietly removed.

Repair a stale view when it is convenient. It is not an outage, and
changing the policy to silence the message is how a diagram that lost
its level filter comes to look exactly like a correct one.
