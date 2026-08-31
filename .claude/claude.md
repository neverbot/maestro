# claude.md

Project context and operating rules for Claude Code (and any other AI
agent working on Maestro). Short and load-bearing; every working
session keeps these invariants in mind from the start.

This file is young: Maestro is in the design phase. Sections marked
**(pending)** are not decided yet — do not treat them as settled, and
update this file as decisions land.

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
- **Deployment:** Docker Compose, image published by CI, reverse
  proxy in front. The primary branch is `master`. A public
  documentation site is built from `docs/site/` by `cmd/maestro-docs`
  and published at `neverbot.github.io/maestro`.
- **Human auth:** local accounts only — email plus argon2id password,
  invite links, no email delivery and no external identity provider.
  Designers are not developers and an instance must work with zero
  external accounts. Registration is `invite_only` or `domain_open`
  against an allowed-domain list.
- **Agent auth:** bearer tokens, one token = one game, admins included.

Decided in the core/metamodel design and detailed there:
`docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md` —
metamodel tables, MCP tool surface, validation and error shapes,
concurrency, single-game navigation.

**(pending)** — visual identity, the D2 view query language, the
renderer catalogue and its graph layout engine, the markdown domain,
the analysis engine, and the agent skill bundle. Each gets its own
spec.

## Operational rules

### Design phase artefacts

- Design specs go to `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`.
- Brainstorming mockups and other throwaway artefacts live under
  `develop/` for now — it is scratch space and may be deleted once
  the design is accepted. Never put throwaway files in `.claude/`
  (read-only context) or at the repo root.
- Markdown filenames are lowercase (`readme.md`, `claude.md`).

### Git

- Commit messages: single line, Conventional Commits. No body, no
  trailers, no `Co-Authored-By`.
- Don't touch `git config`. Don't push without an explicit request.
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

Design phase. No code yet. The repository currently holds this file,
the Nottario skill bundle, and `develop/` scratch space for the
in-progress design conversation.
