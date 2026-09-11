# The query builder: what a person actually composes

**Date:** 2026-09-11
**Task:** Query builder 1, a spike (`1e239f8b`), under "A person cannot
compose a view" (`54705226`).
**Decides:** the shape of the thing, how much of the language it covers,
whether it edits or generates, and where validation sits. No plan and no
code; the plan is the next task.

## 1. The trap, named first

A form that mirrors the query document field by field is the obvious
build and the wrong one. The language has `params`, `from` with per-type
selectors, an ordered `traverse` with `via`, `direction`, `depth`,
`to_type`, `where`, `edge_where` and `as`, plus `nodes`, `edges`,
`project` and `limits`. A faithful form of that is the JSON with boxes
around it: a person can operate it and cannot think in it, and the one
thing they came here able to do — say what they want in a sentence — is
the one thing it will not accept.

The example the whole language was designed around is a sentence:

> *the quests a Mage can reach between level 20 and 30, coloured by zone*

**The builder is closer to that sentence than to the document.** That is
the decision this spike makes, and everything below follows from it.

## 2. The shape: a sentence in clauses

The builder is a stack of clauses that read top to bottom as one
sentence. Each clause is a line of plain words with a small control
where a value goes. Nothing is nested, nothing is a modal, and nothing is
named after a JSON key.

```
Start from    every  [Quest ▾]
Narrow to     where  [level ▾]  [is between ▾]  [20]  and  [30]
Follow        from   [Mage ▾]   through [available to ▾]   [inwards ▾]   up to [1 ▾] steps
Draw          as a   [graph ▾]  coloured by [zone ▾]
```

Read aloud it is the sentence. Read as structure it is `from`,
`where`, `traverse`, `project`. Three properties matter and none of them
is aesthetic:

- **A clause is optional and says so by being absent**, not by being an
  empty box. A query with no traversal has no Follow line; adding one is
  a single "and then follow…" affordance at the bottom of the stack.
- **A value control is a picker over the game's own vocabulary**, never a
  free-text key. Types, relation types, fields and entity keys all come
  from the game; a person choosing "available to" from a list cannot
  misspell `available_to`, and the misspelling is the single most common
  way a hand-written query fails.
- **The words between the controls are fixed and are the product's**, not
  the model's. "through" and "inwards" rather than `via` and
  `"direction": "in"`.

The document stays visible, in a panel beside the sentence, read-only,
updating as the clauses change. Not because a designer will read it, but
because the agent they work with will, and because a builder whose output
is hidden is a builder nobody can check.

## 3. How much of the language ships in v1

**The half a sentence can hold**, and the other half is reachable by
handing the document to an agent. A builder that covers the simple half
and *hides* the other half is worse than one that covers it and says so,
so the boundary is stated on screen, once, under the stack.

In v1:

| Language | In the builder |
|---|---|
| `from`, one or more selectors | yes, as "start from every X" plus "and every Y" |
| `where` on an entity | yes, one predicate stack per selector |
| `traverse`, a **linear** chain | yes, as one or more Follow clauses in order |
| `via`, `direction`, `to_type` | yes, as pickers |
| `depth` as a single maximum | yes |
| `edge_where` | yes, as "where the connection's [field]…" |
| `project.color_by`, `label`, `group_by` | yes, in the Draw clause |
| `limits` | no: server defaults, surfaced as a sentence when hit |
| `params` | no in v1 |
| Branching: two sets from one, referenced by `as` | no |
| `depth` as `{min, max}` | no |
| `nodes` / `edges` explicit selection | no |

**Why branching is out and admitted rather than faked.** `traverse`
requires an explicit `from` precisely so a two-branch query is readable,
and a sentence is linear by construction. A builder that pretended to
branch would need names for sets, and the moment a person is naming sets
they are writing the document with extra steps. The line under the stack
says so: *"A question with two branches, a depth range, or a parameter is
written as a document. Ask an agent for it."*

**Why `params` is out despite being v1 in the language.** A parameter is
how one view serves many subjects, and its value is in `views.run`, not
in composition. The builder's job is the first query; parameterising it
is an edit, and edits are §4.

## 4. It generates; it does not edit

**The builder produces a new query. It never modifies a stored one.**

The copy feature promises a query travels byte for byte, and that promise
is what makes "copy this view and change how it is drawn" safe: the
query is not touched, so it cannot be subtly broken by a round trip
through a form. An editing builder would put a parser and a serialiser
between a stored document and itself, and every such pair eventually
drops a field it did not know about.

So:

- **New view**: the builder composes, `views.upsert` stores exactly what
  the builder emitted.
- **Existing view**: the builder opens it **only if the document
  round-trips byte for byte** through parse and re-emit. That is a
  property the builder can check on the spot, against the stored bytes,
  every time. When it holds, editing is safe and offered. When it does
  not — a branch, a parameter, a field a later language version added —
  the builder says plainly *"This query says more than the builder can
  hold. Copy it and change how it is drawn, or ask an agent to change
  it."* and does not open.
- The round-trip check is a guard, not a claim: it is an assertion in the
  product, not a promise in a document, so a language addition that the
  builder does not learn closes the door by itself rather than silently
  dropping a clause.

**What the copy guarantee now means**, stated because it changed: a
*copied* query is still byte for byte the original. A *built* query is
byte for byte what the builder emitted. A query is never the result of
reading one document and writing a different one.

## 5. Where validation sits

`views.validate` exists to be called repeatedly and answers with a
pointer at the exact position that is wrong. Two consequences:

**Most of what it catches, the builder makes impossible.** An unknown
type key, an unknown relation type, a `via` that does not exist, a
`direction` that is not one of three: all of those are pickers over the
game's own vocabulary, so the builder does not need a diagnostic for them
— it needs to not offer them. A validate call reporting "unknown relation
type" from a builder is a bug in the builder, and that is the line
between the two.

**What is left is what a picker cannot prevent**, and there it runs on
every change, debounced, and its pointer lands **on the clause that
produced that path**. The builder keeps the mapping from each control to
the JSON pointer it writes, which it must have anyway to emit the
document, so the error appears under the control that caused it rather
than in a box at the bottom of the page. A predicate comparing a text
field to a number, a traversal whose `to_type` cannot be reached by that
relation type, a depth above the server's bound: those are the real
diagnostics, and each of them is about one line of the sentence.

A query that does not validate is never storable. The primary button is
disabled with the reason beside it, not enabled into a refusal.

## 6. The pickers, which are the real work

Everything above rests on four pickers over the game's own vocabulary:
entity type, relation type, field of a type, and entity. They are
already owed — the second task under this feature is exactly them — and
they are the reusable part: the entity picker is the same control the
view frame will want for "start from this room", and the field picker is
the same one the Draw clause needs for `color_by`.

Each is a list the product already fetches for other screens, so none of
them is a new endpoint:

- entity types: `GET /types`
- relation types: `GET /relation-types`
- fields: `field_schema` on a type, already read by the catalogue
- entities: `GET /search?type_key=…`, which indexes keys alongside names

## 7. What this spike does not decide

- **Whether the builder lives on its own route or inside the views
  screen.** It depends on whether composing is a task a person leaves
  their reading to do, and that is a layout question for the plan.
- **Whether a built view can be handed to an agent to extend.** It can,
  trivially, because the output is a document; whether the product offers
  that as an action is a product decision.
- **Renderer-specific projection.** `project` differs per renderer
  (`map` wants coordinates, `timeline` wants an axis) and the Draw clause
  will need to change with the renderer picker. The catalogue of what
  each renderer asks for is in the views spec and the plan should read it
  rather than re-derive it here.
