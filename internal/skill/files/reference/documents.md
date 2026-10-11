# Documents

A document is long prose attached to this game's content: a dialogue
script, a zone's lore, the game bible. `docs.write` creates and rewrites
one, `docs.read` reads it back, and `docs.write_many` does several in one
call.

## Send a long document, do not write it out

**`docs.write` charges you the whole document.** Its content is a tool
argument, so every byte is written out through the call and paid for in
your context, and paid for twice when you read the document before
rewriting it. A page of lore is nothing. A game bible is most of a
session.

So for anything of size, hand over a file instead:

1. Write the document to a file.
2. `shasum -a 256 <file>` — the hex digest, which is what the URL gets
   signed over.
3. `docs.upload_url` with the path, the `expected_version` you would
   have passed to `docs.write`, and that digest.
4. `curl -fsS -X PUT --data-binary @<file> '<upload_url>'` — quote it,
   it has an `&` in it, and send **no** Authorization header. The
   signature is the credential; your token stays with you.

The answer names the new version and not the body you just sent.

**Where the line sits**: under about sixteen kilobytes, `docs.write` is
simpler and the difference does not matter. Above that, use the URL. The
hard ceiling is a megabyte on both and is not the reason to choose —
the reason is what the content costs you to emit.

The URL is good for five minutes and for one write, on top of the exact
version you named: the write that lands moves the document past it, so
sending the same URL again answers `version_conflict`. Each refusal says
what to do — `401` means it expired or was edited, so sign another;
`409` means somebody wrote first, so read and decide again; `400` means
the bytes did not match the digest, and nothing was written.

`docs.write_many` is the worst case for all of this, since every body in
the batch goes out through one call. Send the short ones there and the
long ones one URL at a time.

## The line between a field and a document

**Both are markdown and both are rendered, so the choice is never about
what the prose may look like. It is about what each one can do**, and the
two lists are short.

> If a query, a view or an analysis would ever need to read **inside**
> it, it is a field. If you will ever want to know what it said last
> month, to find it as a thing in its own right, or to point several
> entities at the same text, it is a document.

A field is part of its row: `where` reads it, a view draws it, the search
index carries it, `entities.repair` can rewrite it across a whole type.
It has **no history at all** — a write replaces it and what was there is
gone.

A document is a thing with an address: a path, a title, a kind, a version
per write with a message saying why, a diff against any earlier one and a
revert. It is attached to the entities it is about, and it can be
attached to several.

**Length decides nothing.** A quest's objective summary is a field at one
line and still a field at fifteen, because a view draws it. A dialogue
script is a document at any length, because its history is the point and
nothing queries inside it. The old rule here said a field was prose short
enough to sit in a table cell; that was a statement about a screen that
no longer needs it — a row shows the first line and the thing's own page
shows all of it — and it was sending ten honest lines of notes to a
document that did not want them.

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

## Images are attached too, and you do not attach them

An entity can carry image files as well as prose: a map, a reference
picture, a sketch somebody wants beside the thing while they work. They
are attachments in the same sense a document is, and they are not art
for a build.

**Only a person attaches one**, from the entity's page in a browser.
There is no tool here that uploads a file and there will not be one: a
file arrives by somebody choosing it.

What you get is told to you. `entities.get` answers with an `images`
array when there are any, each entry naming the file, its type, its
pixel size and a `download_url`:

- **The bytes are never in the answer.** An image is worth tens of
  thousands of tokens and is almost never what you were asked about.
- **The URL needs no credentials and stops working within the hour.**
  Fetch it with `curl` when the work is actually about the picture, and
  read the entity again for a fresh one.
- A listing does not carry images at all. Read the entity.

If a designer asks you to add a picture, say that you cannot and that
the page for that thing has the control. Telling them to hand you a file
is telling them to do a thing this surface does not admit.
