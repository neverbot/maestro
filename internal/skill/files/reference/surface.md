# Reading the surface itself

Every rule this surface enforces is on the wire before you break it.
The tool descriptions carry the admitted values, the bounds, the
defaults and the combinations that contradict each other, and these
pages deliberately do not copy them: a second copy of a vocabulary is a
copy that goes quietly false.

This page is how to read them, and the two habits that cost the least.

## Where the contracts are

- Over **MCP**: the descriptions arrive with the tool list your client
  already received when it connected. Read the one you are about to
  call.
- Over **REST**: `GET /api/mcp/tools` answers the same names and the
  same text, so an agent driving the mirror is not left guessing, and
  `reference/rest.md` is the table of addresses those tools are mirrored
  at. Both are generated from what the server registers.

**If you have no MCP client, start with `reference/rest.md`.** The
product mirrors every tool as a route on purpose, and until that table
existed an agent holding these pages and an HTTP client could not find
the surface at all: thirty-five probe requests to locate it, and two of
its domains never reached.

`reference/tools.md` is the index of what exists, one line each. It is
generated from the same descriptions, so it can name a tool and never
its contract.

## Post `{}` first

A write you have not used before is cheapest to learn by refusing it:
send `{}` and read the answer. Every required member comes back at its
own path, with what is wrong with it. One round trip teaches the shape.

The alternative is a guess, and a guess costs one round trip per wrong
member.

## Read the refusal whole

Every refusal on this surface reports **every problem it can see at
once**: missing members and unknown ones alike, each at its own path.
Fix all of them before calling again.

An agent fixing one member per round trip on a 200-row seed pays 200
round trips for one bad afternoon, and each of those answers carried the
whole list the first time.

## One reference, three spellings

An entity is named by its type and its key everywhere, and the member
names differ by domain. No tool description says so, because each is
complete on its own:

| Where | The pair |
| :-- | :-- |
| a relation's endpoints | `{type_key, key}` |
| a route's steps | `{entity_type, key}` |
| a document's links | `{entity_type, entity_key}` |

They mean the same thing. Assuming one spelling everywhere is found out
by a `bad_request` naming the member you sent, which works and costs a
round trip each time — read the description, or post `{}`.
