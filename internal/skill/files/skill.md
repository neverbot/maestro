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
