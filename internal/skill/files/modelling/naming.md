# Naming

Keys are the API. Every saved view, every route, every reference from a
document and every future query names your keys as text. A key is the
one part of a game's design that other people's work is written
against.

## The conventions this bundle recommends

These are **bundle conventions**, not server rules. The server accepts
more than this; consistency is what you are buying.

- **Entity type keys are singular.** `quest`, not `quests`; `driver`,
  not `drivers`. The plural has a home of its own in `label_plural`,
  where a display layer can find it.
- **Lower snake is the default spelling.** `quest_line`, `lap_record`.
  The server accepts `quest`, `Quest` and `quest-line` alike; it refuses
  `main quest 1` and `misión-01`. Within one game, pick one spelling and
  keep it: keys are matched without regard to case, so writing `Quest`
  when `quest` is stored is refused naming both spellings rather than
  applied, and you get a round trip and a decision instead of a write.
  A game whose own handles capitalise — `Elwynn_Forest`, `GP_Monaco` —
  should capitalise everywhere.
- **Relation type keys are verb phrases that read source → target.**
  `takes_place_in`, `available_to`, `unlocks`, `connects_to`. Direction
  is literal in every query, and a name like `zone_link` costs every
  future query author a lookup and a guess about which end is which.
- **Never declare a field named `name`, `key`, `type` or `created_at`.**
  It is legal and nothing breaks — the built-in attributes stay reachable
  through their own `@` sigil — but every predicate in the game then
  reads ambiguously to a human, and the human is the one who has to
  decide whether a bug is a bug.
- **Numbers are `number`; ordered categories are `enum` with the options
  in the order the game means.** Both costs are invisible on the day of
  the write and total on the day of the diagram, because a rank, an axis
  and a range filter all read a declared order and none of them can
  invent one.

## What the row-key rule excludes, and why it earns its place

A key that addresses a row admits letters, digits, underscores and
hyphens, starting with a letter or a digit. What it leaves out is
deliberate:

- **No dot, slash, space or percent**, so a key drops into a URL path
  segment and into a query token without escaping and without being
  mistaken for a separator.
- **No leading punctuation**, so no key can read as a flag or an option
  to a shell, a CLI or a parser.
- **ASCII only**, because folding case is not the same operation as
  normalising Unicode: two normalisations of one accented word would
  otherwise coexist as two distinct keys that look identical.

Digits may lead. `1999_season` and `500_miles` are ordinary keys, and no
game should have to rename its content to satisfy an identifier
convention.

## What can be renamed, and what cannot

**An entity's own key is permanent.** There is no rename for a row: the
address an upsert writes against is the address the row was created
with, and the only way to change it is to create a second row and move
everything that pointed at the first — which costs one write per edge,
per attachment and per stored position, and loses the row's history.

**An entity type's key and a relation type's key can be changed.**
`types.rename` and `relation_types.rename` take the key the type has
now, and the type keeps its id, its history, its content and every edge
of it. This is worth knowing precisely, because the workaround the old
surface forced — declare a new type, move every row, delete the old one
— is now both unnecessary and destructive.

What a type rename costs is one thing and it is small: a saved view that
named the old key goes stale. It keeps running, it draws the same
picture — a view records ids, and an id does not move — and it reports
the position in its own stored query document where the old spelling
still sits. Repairing it is one save from a document you already hold.
Nothing rewrites a stored query behind its author, deliberately.

A rename that changes only capitalisation is refused, because the
folding index makes the two spellings one address: it would change
nothing while announcing a change no reader could observe.

So the useful habit is not a ban on renaming. It is:

> **Derive an entity's key from something that will not change**, and
> treat a type rename as a small, visible cost rather than an impossible
> one.

A key derived from a display name is the classic version of getting this
backwards. Titles get rewritten — that is what titles are for — and the
key `the_dark_portal_part_1` outlives the quest that was renamed to
something else in week three, at which point every reader has to know
the old title to find the row.
