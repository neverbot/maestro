---
name: maestro
description: Use when designing the content of a video game in Maestro — declaring a game's own entity and relation vocabulary, seeding characters, places, missions and progression, composing saved views over them, or joining a game somebody else designed.
---

# Maestro skill

Maestro records the **content design** of a game: what a player can be,
where they can go, what they can do and what they unlock. It is not a
task tracker and there is no kanban here.

This page is a placeholder. It exists so the package, its hash and its
budgets can be built and asserted before any prose is written — a budget
added after the page it bounds is a budget that gets raised to fit.

## The one form a quote takes

A tool's description is the contract for one call, and this bundle does
not copy it. Where a page needs the description's own words, it quotes
them, verbatim, attributed, in exactly this form — which the build
checks against the live description on every run:

> **From `entities.upsert`'s own description:**
> Rows are idempotent by (type_key, key), so re-running a seed updates
> in place

Reword that description and this page goes red in the same commit.
Everything a single call can say about itself is in its own description,
which your client already has; what the pages here teach is what spans
calls.

## The four closed vocabularies

These four lists are the only enumerations in this bundle. Everything
else a single call can state about itself lives in that call's own
description. They are here because you choose between them *before* a
description is any use to you, and a round trip to discover what a field
type may be is a round trip in the wrong place.

Each is compared against its Go declaration on every build, in both
directions, so a word that appears in one and not the other is a failing
build rather than a page that quietly went false.

Field types, for a field in a type's schema:

```vocab:field_types
text longtext number bool enum list<text>
```

Semantic roles, for classifying what a relation type *means*:

```vocab:semantic_roles
prerequisite unlock containment spatial availability reward
```

Renderers, for drawing a saved view:

```vocab:renderers
graph layered nested map table timeline
```

Error codes a game-content tool can answer with. Each one names a
different recovery, and the recoveries are what the reference pages
teach:

```vocab:error_codes
unauthorized internal_error not_found bad_request scope_violation
retryable version_conflict schema_violation invalid_schema invalid_input
endpoint_type_mismatch in_use query_invalid renderer_requirements
limit_exceeded query_stale
```
