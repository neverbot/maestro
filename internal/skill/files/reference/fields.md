# Fields

The six field types are enumerated in `skill.md`, because you choose
between them before anything here matters. This page is about the
**validator** — one piece of code, shared by every write on the surface.
Its rules show up in `types.upsert`, `entities.upsert`,
`relation_types.upsert` and `relations.upsert` alike, and each of those
descriptions states them for its own call. What no single call can tell
you is that the same rule is waiting in the next three, and what it will
have cost you by then.

## The rules that surprise people

**A write replaces a row's whole field map, unless it says otherwise.**
By default `entities.upsert` and `relations.upsert` do not merge: a field
the item does not name is cleared, not left where it stood. So an update
either carries the row's whole map — read it back first, from
`entities.get` or `relations.get`, which always answer with fields, or
from the listings with `verbose`, and never from a listing that omitted
them — or it sends `fields_mode: "merge"` and names only the fields it
means to change. In merge mode an explicit `null` clears a field, which
is the only way left to remove one.

The version you send does not protect you from this. It guards against
another writer, not against your own half-built payload: it is the
correct version of a row you are about to empty. `name` is where the
asymmetry gets easy to miss, because it is required and an item that
forgot it is refused, while a field map missing a key looks exactly like
a row that never had one.

**A merge onto an invalid row is refused**, by the stale keys that row
still carries from before a schema edit. That is the repair's job, not an
update's: `entities.repair` and `relations.repair` first, or write the
row whole.

**An unknown field is an error.** It is never silently dropped, in a row
or in a declaration. A key you did not declare comes back refused, and a
typo in a declaration — `defualt`, `has_default`, `lable` — is refused
where you wrote it, not accepted and ignored. Every write on this
surface obeys the same rule in both directions, so there is no call you
can reach for that will quietly take a key nobody declared.

**Changing a schema flags rows; it never edits them.** Add a field to a
type under 200 entities and all 200 are marked invalid, kept and
readable. Nothing is deleted and nothing is back-filled. That is a
three-call sequence and not a failure: `entities.list` or
`relations.list` with the `invalid` filter tells you which rows moved
out from under the declaration, `entities.repair` and `relations.repair`
write the answer into all of them at once, and you call the repair again
until it stops repairing.

**An absent optional field with no default stays absent.** It is not
zero-filled. A view asking `exists` on it will say so, and a `where`
comparing it to a value will match nothing rather than matching the
zero you assumed was there.

**Do not write a value you do not have.** Nothing here asks you to fill
a field to make a row land: an optional field left out is a fact this
product can report, and `required` is the declaration that says the fact
must exist before the row does. A placeholder, a "TBD" or an enum option
meaning "unknown" is the one kind of wrong nothing can detect, because
every check treats it as a decision somebody made — and a reader then
cannot tell "deliberately none" from "nobody has decided yet". If a
value is genuinely unknown at seed time, the field is optional and you
leave it out.

**A default is declared by the presence of the `"default"` key and by
nothing else.** There is no separate flag saying a default exists, on
the wire or anywhere else; the key being there is the whole declaration.
That is what makes a default of `false` or of `0` behave like any other
default, and it is why an explicit `"default": null` declares **no**
default — null is how this surface spells "not set" everywhere.

**`required` and a default together are rejected.** The default lands
before the field could ever be reported missing, so the pair makes
`required` unreachable. You are being asked which of the two you meant,
at the only moment you can still choose.

**Two key rules, not one.** A field key inside a row's values is
`^[a-z][a-z0-9_]*$`; a key that addresses a row — an entity type, a
relation type, an entity — is `^[A-Za-z0-9][A-Za-z0-9_-]*$`. Both are
capped at 64 characters and both are ASCII. `modelling/naming.md` has
the reason the two differ, which is not an inconsistency.

**A label, a colour and an icon are validated too, and their problems
come back together with any key problem in the same answer.** One pass,
one round trip: read the whole list of complaints and fix them all
before calling again. `label` is wanted; `color` is a CSS hex colour and
nothing else; `icon` is a lower-case name, not an image and not an
emoji. Length caps are counted in runes, so an accented label is exactly
as long as its unaccented spelling.

## What a type choice buys you later

The cost of a field type is never paid on the day of the write. Every
one of these lands in a different call, months later, and none of them
can tell you that the mistake was made at declaration time.

- **`text`** holds anything and answers pattern questions. A level
  stored as `text` cannot be range-filtered, cannot rank a layered view
  and cannot be a timeline axis, and it sorts 1, 10, 100, 2.
- **`number`** is the only thing range operators speak, the only thing a
  layered view can rank by directly, and one of the two things a
  timeline axis reads. Store a level, a lap count and a cost as numbers
  even when nobody has asked for a chart yet.
- **`enum`** is an ordered category: its declared options are its order,
  which is what lets a timeline lay them out. Declare the options in the
  order the game means, because a later re-ordering is a schema change
  over every row that carries one.
- **`bool`** answers equality and existence, and nothing else. A
  three-state question modelled as a bool loses its third state on the
  day somebody adds it.
- **`list<text>`** is the whole list family — there is no list of
  numbers and no list of enums — and it is the one field type whose
  membership test is indexed. A tag set belongs here; a set of
  references to other entities does not, because that is a relation.
- **`longtext`** is prose, and **prose here is markdown**. Paragraphs
  separated by a blank line, a line break on a single newline, `**bold**`
  and `_italic_`, `` `code` ``, fenced blocks, bullet and numbered lists,
  quotes, headings and links: all of it is read as markdown by every
  screen that shows the value in full. Write the markdown, not the
  rendering — HTML is not admitted, a tag you write arrives as the
  characters you typed, and a link whose scheme is not `http`, `https` or
  `mailto` arrives pointing at nothing. **`text` is one line and is never
  rendered**, so a value with a line break in it is a `longtext` and not
  a long `text`.

  **Length is not what decides between a field and a document.** Ten
  lines of notes with a list in them are a field. `reference/documents.md`
  draws the line, and it is drawn by what each one can do.

## Where the caps and the exact refusals are

Each write tool's own description carries its own bounds, its own error
codes and the paths they are reported at. Read the description of the
call you are about to make; the point of this page is that reading one
of them tells you about all four.
