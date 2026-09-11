# claude.md

Operating rules for Claude Code, and for any other AI agent working on
Maestro. Short and load-bearing: a working session reads this first and
keeps these invariants from the start.

This file is **public**, like the rest of the repository. It says how
this project is built and verified. It says nothing about the machine
it is built on, nothing about the author's other work, and nothing
about where an instance of it happens to run.

Keep it honest as decisions land. Every sentence here is a claim about
code that can go false with nothing turning red — which happened
repeatedly during the build, and cost more than any compiler error did.

## What Maestro is

Open-source, self-hosted service that coordinates human designers and
their AI agents while **designing the content of a video game**:
narrative, missions, characters, places and progression.

Maestro is *not* a project tracker. There is no kanban, no gantt, no
sprint. What it tracks is the *game design*: everything a player can
be, go to, do and unlock.

The core is a **pure metamodel**. Maestro ships no built-in notion of
"class", "zone" or "quest". Each game declares its own:

1. **EntityType** — a named kind of thing, with a field schema
   (`Class`, `Zone`, `Quest`, `Talent` for an MMORPG; `Driver`, `Car`,
   `Circuit`, `Race`, `Championship` for a racing career game).
2. **Entity** — an instance of an entity type.
3. **RelationType** — a named kind of directed edge
   (`takes_place_in`, `requires`, `connects_to`, `unlocks`,
   `available_to`, `rewards`).
4. **Relation** — an instance of a relation type between two
   entities.

Everything else — the catalogues, the place graph, the mission lists
with preconditions and rewards, the progression trees — is a **view**
over those primitives, driven by how a game classifies its own relation
types. Genericity is the product: the same schema must serve an MMORPG
and a racing career equally well.

Agents are not expected to guess the metamodel. Maestro ships a **skill
bundle** teaching them how to declare types and populate content, with
worked examples per genre.

## The repository is PUBLIC

Everything committed here is world-readable, forever, including in
history after a later deletion.

**Before every `git commit`, review what is being staged**, with this
in mind:

- No secrets of any kind: API keys, bearer tokens, passwords, session
  keys, certificates, SSH keys, `.env` contents, database URLs with
  credentials.
- No private infrastructure: hostnames, IP addresses, ports of a real
  deployment, container names, dashboard URLs, VPN addresses.
- No personal data: private email addresses, real names of third
  parties, chat excerpts.
- No local absolute paths that expose a home directory — write
  repo-relative paths.
- No references to a contributor's other projects, private or public,
  and no instructions that only make sense on one person's machine.
- No scratch artefacts: brainstorming dumps, debug output, screenshots.
  Those live in ignored directories.

Prefer `git add <specific files>` and read the diff before committing.
If something questionable is already committed, say so immediately —
rewriting history on a public repo is a decision for the human.

## Language policy

**All written artefacts are in English.** Source, comments, identifier
names, `docs/`, `readme.md`, `changelog.md`, commit messages, seeded
markdown, issue and PR titles and bodies, default UI strings, design
specs and plans.

Conversation with the user happens in whatever language they choose;
artefacts written to disk are English regardless.

## Technical invariants

- **Lightweight is a first-order goal.** Single binary with embedded
  assets, small Docker image, no heavy build pipeline. Every dependency
  justifies its presence.
- **Backend:** Go, `net/http` (no framework), pgx/v5, sqlc for all
  queries, embedded goose migrations.
- **Database:** PostgreSQL. `jsonb` for user-declared field values,
  `tsvector` + GIN for search, recursive CTEs for graph walks.
- **Real-time:** SSE, no WebSockets.
- **MCP:** served over HTTP+SSE from the same binary, authenticated
  with per-game bearer tokens. One token = one game.
- **Frontend:** vanilla CSS + Lit, ES modules, **no build step**, no
  TypeScript. Graph layout by a vendored layout engine; rendering is
  ours in SVG. Two consequences that have each cost a day:
  - **`default-src 'self'`.** A `<style>` element built by script is
    refused silently; a shadow root gets its CSS through
    `adoptedStyleSheets`. Fonts are self-hosted for the same reason.
  - **The design system stops at the shadow boundary.** Element
    selectors in the global stylesheet do not cross into a shadow
    root; custom properties do. A component states its own shape and
    reads tokens.
- **Human auth:** local accounts only — email plus argon2id password,
  invite links, no email delivery and no external identity provider.
  Designers are not developers and an instance must work with zero
  external accounts. Registration is `invite_only` or `domain_open`
  against an allowed-domain list.
- **Agent auth:** bearer tokens, one token = one game, admins included.
- **Deployment:** Docker Compose, a reverse proxy in front. The primary
  branch is `master`. CI builds the image, smoke-tests it against a
  real Postgres, and publishes nothing: building from this repository
  is the only way to run it. The binaries are `maestro` and
  `maestro-skilldoc`.

## The design system

Three files, and they move together:

- `docs/product.md` — who this is for, the tone, the anti-references.
- `docs/design.md` — the normative tokens and the named rules
  (Two Grounds, Ink Button, Named Absence, Shadow Boundary, and the
  rest). The stylesheet answers to this document, not the reverse.
- `docs/design-tokens.json` — canonical OKLCH values, shadows, motion,
  and the shared components' own HTML and CSS.

`docs/design-system.html` renders all three and is **generated** by
`docs/design-system.mjs`. Edit the sources and regenerate; never edit
the page.

Interface work is analysed through the `impeccable` skill rather than
by eye. Its loader looks for `product.md` and `design.md` at the repo
root, then `.agents/context/`, then `docs/` — which is why they live in
`docs/`.

## Operational rules

### Documents

- Specs and plans live in `.superpowers/`, which is **git-ignored**:
  the working record of how this was built, never published. A claim
  that has to survive is written where the code is.
- Design specs: `.superpowers/specs/YYYY-MM-DD-<topic>-design.md`.
  Implementation plans: `.superpowers/plans/YYYY-MM-DD-<topic>.md`.
- **Record a correction where the decision lives**, not in a commit
  message: a commit message is read once, and the code beside it is
  read by whoever changes the area next.
- Throwaway artefacts (screenshots, probe pages, scratch dumps) go in
  `.scratch/`, which is git-ignored. Never in `.claude/` (read-only
  context) and never at the repo root.
- Markdown filenames are lowercase (`readme.md`, `claude.md`).

### How this project verifies itself

Not style preferences. Each one is here because skipping it shipped a
defect during the build.

- **Mutation is the proof.** A green test proves nothing until you have
  seen it go red: introduce the fault the test claims to prevent,
  confirm by diff that the patch landed, watch it fail, restore.
  Nearly every serious defect found here was found this way, and none
  by reading a diff.
- **Run the one test, not the package.** A single test is under five
  seconds; `internal/web` alone is over three minutes.
- **`export TEST_DATABASE_URL` or hundreds of tests skip in silence**
  and `go test` still prints `ok`. A repo-wide `-v` run must show zero
  `--- SKIP` lines.
- **Correct in the module, dead at the call site.** The most repeated
  defect here: a unit asserted by a harness that calls it directly,
  wired to nothing, everything green. Assert through the path the
  product actually uses — the real transport for a tool, the real page
  for a component.
- **A rule established and not carried one step along.** The second
  most repeated, and it lands *inside the correction that establishes
  the rule* more often than not. When you write a rule, find every
  place it applies before you stop.
- **A mechanism nothing reads is a lie.** No knob without a reader, no
  signal nobody consumes, no cap whose overflow nothing detects.
- **Prose about code is code, and rots the same way.** Doc comments,
  tool descriptions and the skill bundle have each shipped statements
  contradicting the code with nothing red anywhere. Check a claim
  against the code, never against a spec — the spec is older.
- **A browser fails silently where a server returns an error.** A
  refused stylesheet, a blocked import map and an unwired listener all
  look exactly like working code. Frontend work is not verified until
  something has opened it.

### Git

- Commit messages: single line, Conventional Commits. No body, no
  trailers, no `Co-Authored-By`.
- Don't touch `git config`.
- **Never `git push`, and never ask about pushing.** Work ends at the
  commit; the human pushes. Report the commit and what is unpushed.
- No `--no-verify`, `reset --hard`, `clean -f`, `branch -D`, amend or
  any history rewrite unless explicitly asked.
- Prefer `git add <specific files>` over `git add -A`.

### Externally visible actions

Push, PR and issue comments, messages, uploads to third-party services:
each needs explicit human confirmation. A one-time approval does not
extend to future calls.

### Live databases are sacred

Never drop, wipe or recreate a database holding real user state
(`docker compose down -v`, `docker volume rm`, `DROP DATABASE`,
unqualified `TRUNCATE` or `DELETE`). To verify a migration, create a
fresh throwaway database from a test helper or a separate compose
project. If you cannot proceed without resetting live data, stop and
ask.

## Project status

All seven sub-projects have shipped. `make check` is the gate: gofmt,
vet, lint, `sqlc diff`, the skill-bundle guards and
`go test -race ./...`.

| | What is there |
|---|---|
| Core | Server, Postgres, identity, invites, sessions, tokens |
| Metamodel | The four primitives, field schemas, validation, search, bulk writes, repair, rename |
| Markdown | Versioned prose, diffs, moves, attachments to entities |
| Views | Query language, saved views, staleness, positions, background images |
| Interface | Six renderers drawing, its own visual identity, a text twin |
| Analysis | Cycles, unreachable content, orphans, routes with a verdict |
| Skills | The agent bundle, three genres, served over a signed URL |

The agent surface is MCP tools, mirrored route for route in REST.

The interface is being redesigned against `docs/design.md`: a shared
header and breadcrumb, one row component behind every catalogue, one
statement of what a control looks like shared by the Lit components,
and the analysis screens written as reports rather than as tables.
Guards in `internal/web/static_*_test.go` hold the vocabulary, the
tokens and the control styles.

### What is deliberately not built

Say these plainly rather than letting someone discover them:

- **A human alone cannot compose a view.** There is no query builder —
  the query language was written for agents, and building one is its
  own sub-project. A person can copy an existing view and change how it
  is drawn, and nothing more.
- **No published image, no documentation site, no backups.**
- **One process only.** Events fan out from an in-memory hub, not
  Postgres `LISTEN`/`NOTIFY`, and the rate limiters are in-process, so
  a second replica has its own subscribers and its own budgets.
- **Nobody has verified that the skill bundle teaches.** Its guards
  prove it is consistent with the server and say nothing about whether
  an agent reading it can start. A cold-read rehearsal built a working
  game and never derived the route checker, which is a gap in the
  bundle and not in the reader.
