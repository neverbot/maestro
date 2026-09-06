# A worked example: a racing career

A career mode: drivers, cars you buy and upgrade, circuits, races,
championships that run over several rounds, and licences that gate what
you may enter. The transcript beside this page — `genres/racing.json` —
is the example as an ordered list of calls, applied to a real game by a
test on every build.

The vocabulary is not the product. The reasoning is. A career game with
teams, sponsors and a repair budget will declare different types and
answer the same four questions.

The entity types it declares:

```vocab:genre_types_racing
driver car upgrade circuit race championship licence
```

And four relation types: `contains`, `takes_place_in`, `requires`,
`unlocks`.

## The hard problem: two edges that both sound like "belongs to"

A championship *contains* races. A race *needs* a licence. In English
both are "belongs to", and an agent that models them with one edge type
produces a game where neither question can be asked.

They are different because **they are walked differently**:

- `contains` is read **downwards**: give me the rounds of this season,
  in order. It is a containment edge, and a season is a list.
- `requires` is read **upwards**: what stands between the player and
  this race. It is a prerequisite edge, and the answer is a chain that
  keeps going — licence A needs championship wins, which need licence B.

The direction rule decides the spelling. `contains` runs championship →
race, `requires` runs race → licence, and both read source-to-target as
written. A query walking `contains` outward from a championship gets its
calendar; a query walking `requires` outward from a race gets its
entry conditions. Neither walk can be recovered from the other, which is
the practical reason they are two types rather than one with a flag.

## The round number lives on the edge

Round three of the Club Series is not a property of the race and not a
property of the championship. The same race can be round two of one
season and round five of another, and a `round` field on the race would
hold one of those numbers and lose the other.

So `round` is a field on `contains`, which is law 2 doing exactly what
it is for. It is also why the same edge type joins a car to its fitted
upgrades with no round at all: an edge field that does not apply to
every edge of the type is left unset rather than invented.

## `requires` and `unlocks` are one gate seen from two ends

A licence gates the races above it; finishing a championship grants the
next licence. Those are the same progression read forwards and
backwards, and it is tempting to declare one relation type that means
both.

It cannot be one type. A single type declaring both behaviours gates in
both directions at once, and that combination is refused by name when
you declare it — which is the surface stopping you from writing a graph
that walks in circles. Two types, one for each direction:

| Edge type | Runs | Meaning | Behaviour |
|---|---|---|---|
| `requires` | race → licence | prerequisite | prerequisite_of |
| `unlocks` | championship → licence | unlock | unlocks |

Read that table as one progression. The player wins the Club Series,
which `unlocks` Licence C; National GT `requires` Licence C. The chain
crosses between the two edge types at every step, and that is what a
progression *is* in this model — not one long edge type, but two that
alternate.

## Drivers hold licences too

`requires` also runs driver → licence, and that is the same edge type
rather than a fifth one because it is the same question: what does this
thing need before it can take part. A separate `driver_licence` type
would have meant two walks for "who and what can enter this race".

The endpoint declaration keeps it honest: `requires` admits `race`,
`championship`, `driver` and `upgrade` at the source end and `licence`
and `upgrade` at the target end, so an edge from a circuit to a car is
turned away at write time rather than sitting in the game as a shape
nobody meant.

## Upgrades chain to themselves

Stage two of the engine needs stage one. Source and target are both
`upgrade`, which is ordinary: a relation type's endpoints are two lists
of type keys and nothing stops the same key appearing in both.

The stage number is a field on the upgrade — it is a property of that
part, the way a talent's tier is in `genres/mmorpg.md` — and the
prerequisite is an edge, for the same reason it is there.

## Where this differs deliberately

**Against `genres/mmorpg.md`, on how a place is reached.** That game's
place graph is a graph: `connects_to` joins zones to zones, and the
question it answers is what is adjacent to what. This game's places form
no graph at all — a circuit is not next to another circuit, and there is
nothing to walk. `takes_place_in` runs race → circuit and stops.

The consequence is worth stating, because it is the sort of thing a
worked example teaches by accident: **a game does not need a place graph
just because it has places.** Declaring `connects_to` here would have
produced an edge type with no edges, which is vocabulary a reader has to
learn and cannot use.

**Against `genres/metroidvania.md`, on where progression lives.** The
metroidvania holds its whole progression in one relation type, with the
gate as a field on the crossing. This game splits progression across
`requires` and `unlocks` because its gates are *objects the player
holds* — a licence is a row, with a grade and a test count, and it is
listed in the career screen. A door's required ability is not an object;
it is a condition on one crossing.

## Where to look next

- `modelling/deciding.md` — one relation type with a field, or two
  relation types.
- `modelling/naming.md` — why `contains` and `requires` are spelled
  source-to-target.
- `recipes/joining-a-game.md` — reading a game like this one back before
  writing to it.
