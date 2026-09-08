# claude.md

Project context and operating rules for Claude Code (and any other AI
agent working on Maestro). Short and load-bearing; every working
session keeps these invariants in mind from the start.

All seven sub-projects have shipped. What was pending here is now
decided, built and tested; the specs and plans under
`docs/superpowers/` are the record of how, and the "Project status"
section at the end says where things actually stand.

Keep this file honest as decisions land. Every sentence in it is a
claim about code that can go false without anything turning red —
which happened repeatedly during the build, and cost more than any
compiler error did.

## What Maestro is

Open-source, self-hosted service that coordinates human designers and
their AI agents while **designing the content of a video game**:
narrative, missions, characters, places and progression.

Maestro is *not* a project tracker. There is no kanban, no gantt, no
sprint. Task tracking for building Maestro itself lives in Nottario
(see "Sibling project" below). What Maestro tracks is the *game
design*: everything a player can be, go to, do and unlock.

The core is a **pure metamodel**. Maestro ships no built-in notion of
"class", "zone" or "quest". Each game project declares its own:

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
over those primitives, driven by how a project classifies its own
relation types. Genericity is the product: the same schema must serve
an MMORPG and a racing career equally well.

Agents are not expected to guess the metamodel. Like Nottario,
Maestro ships a **skill bundle** teaching agents how to declare types
and populate content, with worked examples per genre.

## The repository is PUBLIC

`github.com/neverbot/maestro` is a public repository. Everything
committed here is world-readable, forever, including in history after a
later deletion.

**Before every `git commit`, review what is being staged**, with this
in mind:

- No secrets of any kind: API keys, bearer tokens, passwords, session
  keys, certificates, SSH keys, `.env` contents, database URLs with
  credentials.
- No private infrastructure details: internal hostnames or IPs, VPN
  addresses, ports of the author's home server, container names of
  private deployments, dashboard URLs.
- No personal data: private email addresses, real names of third
  parties, chat excerpts, customer content.
- No local absolute paths that expose a home directory
  (`/Users/<name>/…`) — write repo-relative or `~`-prefixed paths.
- No scratch artefacts: brainstorming dumps, debug output, screenshots
  of private tools. Those live in ignored directories.

Prefer `git add <specific files>` and read the diff before committing.
If something questionable is already committed, say so immediately —
rewriting history on a public repo is a decision for the human.

## Language policy

**All written artefacts are in English.** Source, comments, identifier
names, `docs/`, `readme.md`, `changelog.md`, commit messages, seeded
markdown, issue/PR titles and bodies, default UI strings, design specs
and plans.

Conversation with the user happens in whatever language they choose
(usually Spanish); artefacts written to disk are English regardless.

## Sibling project: Nottario

Maestro's architecture is deliberately modelled on
[Nottario](../nottario/) (`~/Projects/neverbot/nottario`): single Go
binary, embedded assets, Postgres, MCP over HTTP+SSE, per-project
tokens, self-hosted via Docker. Read Nottario's `.claude/claude.md`
and `docs/initial/` before proposing structural changes — most
questions about "how should this be built" already have an answer
there.

Two things must **not** be copied:

- **The domain.** Nottario tracks work; Maestro tracks game content.
  No tasks, no cycles, no priorities, no kanban, no gantt.
- **The visual design.** Nottario is deliberately GitHub-like.
  Maestro gets its own identity, designed through the `impeccable`
  skill. Do not reuse Nottario's palette or component look.

**Maestro's own development work is tracked in Nottario**, project
slug `maestro`, over the `mcp__nottario__*` tools (roles: backend,
frontend, qa, design). File work before doing it, claim atomically
with `tasks.claim_next`, link commits, close with a one-line comment.
The Nottario skill bundle is installed at `.claude/skills/nottario/`
and is the authority on that workflow.

## Technical invariants

Inherited from Nottario unless a design decision overrides them:

- **Lightweight is a first-order goal.** Single binary with embedded
  assets, small Docker image, no heavy build pipeline. Every
  dependency justifies its presence.
- **Backend:** Go, `net/http` (no framework), pgx/v5, sqlc for all
  queries, embedded migrations.
- **Database:** PostgreSQL. `jsonb` for user-declared field values,
  `tsvector` + GIN for search, `LISTEN`/`NOTIFY` for real-time,
  recursive CTEs for graph walks.
- **Real-time:** SSE, no WebSockets.
- **MCP:** served over HTTP+SSE from the same binary, authenticated
  with per-project bearer tokens. One token = one project.
- **Frontend:** vanilla CSS + Lit, ES modules, no build step, no
  TypeScript. Graph layout by a vendored layout engine; rendering is
  ours in SVG.
- **Deployment:** Docker Compose, reverse proxy in front. The primary
  branch is `master`. **Not yet true, and written here so nobody
  repeats it as if it were:** CI builds the image and smoke-tests it
  against a real Postgres, and pushes it nowhere — building from this
  repository is the only way to run it. There is no documentation site
  and no `cmd/maestro-docs`; the binaries are `maestro` and
  `maestro-skilldoc`.
- **Human auth:** local accounts only — email plus argon2id password,
  invite links, no email delivery and no external identity provider.
  Designers are not developers and an instance must work with zero
  external accounts. Registration is `invite_only` or `domain_open`
  against an allowed-domain list.
- **Agent auth:** bearer tokens, one token = one game, admins included.

Each area has a spec under `docs/superpowers/specs/` and a plan under
`docs/superpowers/plans/`. **The plans are worth more than the specs
now**: every one carries a "Corrections made during implementation"
block recording what the spec got wrong, and four end with a "What
this sub-project learned" section. Read the corrections before
changing an area — they are where the reasoning behind the odd-looking
decisions lives.

| Area | Spec | Plan |
|---|---|---|
| Core, metamodel | `2026-08-31-core-and-metamodel-design.md` | `2026-08-31-core.md`, `2026-08-31-metamodel.md` |
| Markdown | `2026-09-02-markdown-domain-design.md` | `2026-09-02-markdown.md` |
| Views | `2026-09-02-views-and-query-language-design.md` | `2026-09-02-views.md` |
| Interface | `2026-09-06-interface-design.md` | `2026-09-06-interface.md` |
| Analysis | `2026-09-02-analysis-engine-design.md` | `2026-09-06-analysis.md` |
| Skill bundle | `2026-09-02-agent-skill-bundle-design.md` | `2026-09-06-skills.md` |

## Operational rules

### Documents

- Design specs: `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`.
- Implementation plans: `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`,
  each carrying its own corrections block. **Record a correction where
  the decision lives, not in a commit message** — a commit message is
  read once and a plan is read by whoever changes the area next.
- Throwaway artefacts (screenshots, probe pages, scratch dumps) go in
  `.scratch/`, which is git-ignored. Never in `.claude/` (read-only
  context) and never at the repo root.
- Markdown filenames are lowercase (`readme.md`, `claude.md`).

### How this project verifies itself

These are not style preferences. Each one is here because skipping it
shipped a defect during the build.

- **Mutation is the proof.** A green test proves nothing until you have
  seen it go red: introduce the fault the test claims to prevent,
  confirm by diff that the patch actually landed, watch it fail, and
  restore. Nearly every serious defect found in this repository was
  found this way, and none by reading a diff.
- **Run the one test, not the package.** A single test is under five
  seconds; `internal/web` alone is over three minutes. Mutation against
  a whole package costs forty times what it needs to.
- **`export TEST_DATABASE_URL` or hundreds of tests skip in silence**
  and `go test` still prints `ok`. A repo-wide `-v` run must show zero
  `--- SKIP` lines.
- **Correct in the module, dead at the call site.** The single most
  repeated defect here: a unit asserted by a harness that calls it
  directly, wired to nothing, with everything green. Assert through the
  path the product actually uses — the real transport for a tool, the
  real page for a component.
- **A rule established and not carried one step along.** The second
  most repeated, and it lands *inside the correction that establishes
  the rule* more often than not. When you write a rule, find every
  place it applies before you stop.
- **A mechanism nothing reads is a lie.** No knob without a reader, no
  signal nobody consumes, no cap whose overflow nothing detects.
- **Prose about code is code, and rots the same way.** Doc comments,
  tool descriptions and the skill bundle have all shipped statements
  that contradicted the code with nothing red anywhere. Check a claim
  against the code, never against the spec — the spec is older.
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

Push, PR/issue comments, messages, uploads to third-party services:
each needs explicit human confirmation. A one-time approval does not
extend to future calls.

### Live databases are sacred

Never drop, wipe or recreate a database holding real user state
(`docker compose down -v`, `docker volume rm`, `DROP DATABASE`,
unqualified `TRUNCATE`/`DELETE`). To verify a migration, create a
fresh throwaway database from a test helper or a separate compose
project. If you cannot proceed without resetting live data, stop and
ask.

## Project status

All seven sub-projects have shipped. `make check` is the gate:
gofmt, vet, lint, `sqlc diff`, the skill-bundle guards and
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
  an agent reading it can actually start. Two acceptance cases needing
  a human are recorded as not run.
