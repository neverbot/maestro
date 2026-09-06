# Errors

Every code below names a **different recovery**. Getting the code right
and the recovery wrong costs a round trip; getting the code wrong costs
an afternoon of looking in the wrong place.

```vocab:error_codes
unauthorized internal_error not_found bad_request scope_violation
retryable version_conflict schema_violation invalid_schema invalid_input
endpoint_type_mismatch in_use query_invalid renderer_requirements
limit_exceeded query_stale
```

## Fix everything in one pass

Every refusal on this surface reports **every problem it can see at
once**, at the path of each one. Read the whole list and fix all of it
before calling again. An agent fixing one typo per round trip on a
200-row seed pays 200 round trips for one bad afternoon, and each of
those round trips was answered with the complete list the first time.

## The three that look alike

This is the headline of the page, because the three codes that sound
interchangeable send you to three different places.

| Code | What happened | Where to look |
|---|---|---|
| `invalid_schema` | a **declaration** cannot stand — an unknown field type, an enum with no options, a required field that also has a default | the type or relation type you are declaring |
| `schema_violation` | a **row of values** does not fit a declaration that stands perfectly well | the fields of the entity or edge you are writing |
| `invalid_input` | the **call's own arguments** are malformed — a key with a space in it, a bad colour, an unreadable icon, a bound over its cap | the argument the path names, and nothing else |

A key with a space in it is `invalid_input`. An agent that reads it as
`schema_violation` goes looking at entity values, which is the one place
the problem is not.

## The rest, with their recoveries

| Code | What happened | What to do |
|---|---|---|
| `unauthorized` | the token is missing, wrong or expired | stop and ask the human; no retry helps |
| `scope_violation` | the call named a game this token is not bound to | stop; one token addresses one game |
| `bad_request` | the call is malformed before any domain saw it | fix the call's shape |
| `not_found` | the address names nothing — including a version claim for a row that is gone | re-create deliberately, with no version claim, or fix the address |
| `version_conflict` | somebody wrote between your read and your write | re-read, merge onto the version reported, write again |
| `endpoint_type_mismatch` | an edge's endpoint is of a type its relation type does not admit | fix that end, or widen the relation type's endpoint list |
| `in_use` | a removal would strand content | remove the content first, or cascade deliberately |
| `query_invalid` | the query document itself is wrong, at the pointer given | fix that position and validate again |
| `renderer_requirements` | the query and the renderer disagree about what is drawn | change the query, or change the renderer |
| `limit_exceeded` | a declared limit is above its hard cap; it is refused rather than lowered for you | lower the number you sent |
| `query_stale` | the design moved under a saved view | repair the view; do not switch the stale policy to make the message go away |
| `retryable` | the database refused the call over contention, not over anything you sent | send the same call again, unchanged, once |
| `internal_error` | the server broke | report it; nothing you change makes this one go away |

## Two that are not what they look like

**`retryable` is the only recovery that is "change nothing".** Resend the
identical call. If it keeps coming back, the call is too expensive as
written rather than unlucky, and the second recovery is to ask for less
— a smaller batch, a narrower filter, a shallower walk. On a view run the
message names the bounds that were in force, which is the number to
lower.

**`query_stale` is not an outage.** A design that moved under a saved
view is the normal case of a game being designed. The error carries a
pointer into the view's own stored document and the spelling that
drifted; `reference/queries.md` is where that is worked through.
