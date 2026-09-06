# Documents

A document is long prose attached to this game's content: a dialogue
script, a zone's lore, the game bible. `docs.write` creates and rewrites
one, `docs.read` reads it back, and `docs.write_many` does several in one
call.

## The line between a field and a document

> If a query, a view or an analysis would ever need to read **inside**
> it, it does not belong in a document.

`longtext` is for prose short enough to sit in a table cell. A quest's
objective summary is a field, because somebody will want to filter or
draw it; its dialogue script is a document, because nobody will ever
query inside it and it would make every listing of that type expensive.

Frontmatter is stored and echoed back, never interpreted. It declares no
fields and creates no attachments, so nothing you write there becomes
queryable by having been written there.

## Links are replaced, not merged

The trap on this surface, and the reason it is on a page rather than
only in a description: writing a document **with** a `links` argument
replaces its whole attachment set, and writing without one leaves the
attachments alone.

That is a cross-call consequence. The links you would lose were attached
by a *different* call, earlier and possibly by somebody else —
`docs.links.add` and `docs.links.remove` edit them one at a time and do
not touch the document's version. So the safe sequence when you mean to
add one attachment to an existing document is `docs.links.add`, not a
rewrite carrying the links you happen to remember. If you do rewrite,
read the current set first with `docs.links.list` and send it whole.

## Versions are the point of the domain

Nothing here is overwritten. Every save is a snapshot with an author and
a message, and the sequence that pays for itself is: `docs.history` to
see what happened, `docs.diff` to see what changed between two of them,
`docs.read_version` to read one as it stood, `docs.revert` to bring one
back as a new version. A revert is a write like any other; the history
keeps growing forwards.

A deletion is soft. The history survives it, and writing to the same
path again brings the document back with its numbering continuing. This
is why `docs.delete` is not a decision that needs agonising over, and
why a path is worth choosing carefully anyway: `docs.move` changes it,
and every reader who bookmarked the old one is on their own.

## Kinds are a vocabulary the game invents

Maestro ships no document kinds, the same way it ships no entity types.
A kind is free text, folded to lower case, and the game's own set of
them is whatever its documents have used. Call `docs.kinds` **before**
filtering `docs.list` or `search` by one: filtering by a kind nobody
used answers with an empty page rather than telling you that you guessed
wrong, which is the same silent-empty failure `reference/queries.md`
warns about in view queries.
