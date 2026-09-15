# The site's screenshots, and how to make them again

Every image the documentation site publishes lives in this directory,
is committed, and has a row in the index below.

**They are the one thing on the site that goes stale without anybody
editing it.** Prose contradicting the code is caught by a guard or by a
reader; a screenshot of a screen that has since been redesigned is
simply wrong, and looks right. The product reached its first usable
version and is about to be used in anger, which is exactly when screens
move — so these are expected to be replaced, and the index exists so
that replacing them is a checklist rather than an archaeology.

## The recipe

Every image here was made this way. Following it again gives a set that
matches, which is the point: two screenshots taken at different widths
in different themes look like two different products.

1. **A real instance with real content.** `make dev`, then
   `DEMO_SLUG=demo make demo`, which seeds the game these shots use: a
   quest chain that gates itself, three hundred creatures, two views and
   a document. Nothing here is mocked up.
2. **Sign in** as the account `make dev` prints.
3. **1440 by 900, light theme.** The width the frame is designed at
   (`docs/design.md`: content 1440 maximum), and the theme the identity
   is written for. A dark-theme shot beside a light-theme one reads as a
   bug.
4. **The viewport, not the full page.** A full-page capture of a
   three-hundred-row catalogue is a mile of table nobody can see.
5. **Save it here** under a name that says what it shows, not where it
   came from. `quest-chain.png` names a picture; a timestamp names a
   download.
6. **Add its row to the index below**, and say what it shows and when it
   was taken. `TestEveryImageIsInTheIndex` fails on an image that has no
   row and on a row that has no image.
7. Rebuild the site (`make docs`) and look at the page that carries it.

## The index

Two images, both taken on 2026-09-15 from the `demo` game at 1440x900
in the light theme.

| File | Shows | Taken from |
| :--- | :--- | :--- |
| `quest-chain.png` | A saved view drawn as a graph: ten quests, the edges that gate them, and a legend colouring them by faction. | `/g/demo/v/quests` |
| `catalogue.png` | The catalogue of one declared type: three hundred creatures, their keys and one declared field, sorted by the column headings. | `/g/demo/t/creature` |

## Where they are used

`cmd/maestro-docs` copies this directory into the built site under
`images/`, so a page refers to one as `images/quest-chain.png` (or
`../images/…` from a page one directory deep). Nothing else copies them
and nothing resizes them.

**A picture on its own line is a figure, and the italic line under it is
its caption.** That is the whole convention, and it exists because
markdown has no figure and this site's renderer admits no raw HTML to
write one with:

```markdown
![What it shows, for a reader who cannot see it.](images/quest-chain.png)

*What it is, for a reader who can.*
```

The `width` and `height` are read out of the PNG itself when the site is
built, so a picture holds its own space before it loads and nothing
jumps. Nobody writes a size down, and nobody has to correct one.
