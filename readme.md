# Maestro

**A workspace where game designers and their AI agents build the design
of a game together: characters, places, missions, progression, story.**

[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](license.md)
![Status: early development](https://img.shields.io/badge/status-early%20development-orange)

Self-hosted and open source. Maestro holds the *design* of a game, never
a running one: no live instances, no real players, no telemetry.

A game declares its own vocabulary, fills it with content, and Maestro
answers questions nobody can answer by reading: what is unreachable,
what depends on itself, what nothing points at. People work in the
browser; their agents work over MCP against the same data.

---

## What it is for

You are designing a game that has grown past what one person can hold at
once: four hundred missions, a progression nobody can verify by eye, a
zone somebody renamed last week. Maestro is where that design lives.

**Catalogues.** Every class, every circuit, every mission the game
contains, listed by type, searchable.

**Views.** A saved diagram is a query plus a layout. A world map with
real coordinates over a background image, a layered progression tree, a
table, a timeline. Ask for *the quests a Mage can reach between level 20
and 30, coloured by zone*, and that is a view rather than a feature
request. Every diagram also has a text twin that describes the same
answer for a reader who cannot see it.

**Analysis.** Prerequisite cycles. Content no player can ever reach.
Orphans. Saved routes such as *Mage levelling 1-20*, checked to prove a
progression still holds together.

**Prose.** Lore and mission scripts as versioned markdown, attached to
the entities they describe.

**And what a person can change themselves.** Most writing here is an
agent's job — declaring types, writing relations, filling a game — but
three things are edited where they are read: an entity's name, an
entity's field values, and a document's body. Every one of them states
the version it was read at, so a save that lands on somebody else's is
shown as a conflict with both values named rather than quietly winning.
A view can also be composed in the browser, clause by clause, for the
half of the query language a sentence can hold.

## Maestro knows nothing about games

No quest table. No zone table. No character table. Every game brings its
own vocabulary, built from four primitives:

| Primitive      | What it is                                   |
| :------------- | :------------------------------------------- |
| `EntityType`   | a kind of thing, with a field schema          |
| `Entity`       | one instance of that kind                     |
| `RelationType` | a kind of directed edge, with its own schema  |
| `Relation`     | one edge, from one entity to another          |

That is the whole model, and a genre nobody anticipated has to fit
without a code change.

| Genre             | Declares                                     | Connected by                                  |
| :---------------- | :------------------------------------------- | :-------------------------------------------- |
| **MMORPG**        | `Class` `Zone` `Dungeon` `Quest` `Talent`     | `connects_to` `takes_place_in` `requires`      |
| **Racing career** | `Driver` `Car` `Circuit` `Race` `Licence`     | `unlocks` `contains` `requires`                |
| **Metroidvania**  | `Room` `Ability` `Boss` `Item`                | `connects_to`, carrying its own `is_one_way`   |

That last row is the point of typed edges: whether a door swings both
ways belongs to the door, not to either room, so `is_one_way` is a
declared field on the relation type and is validated on every write.

## Agents

Agents drive the same data over **MCP**, with every tool mirrored in
REST, and they learn the metamodel from a skill bundle the instance
serves them: `skill.install` hands back a short-lived download and the
bundle's version, so an agent arrives knowing how to declare types and
seed a few hundred entities without being told twice. One token is one
game.

## Running it

Requires Docker. Every push to master that passes the gate publishes
`ghcr.io/neverbot/maestro:latest`, so a server can pull it:

```bash
docker pull ghcr.io/neverbot/maestro:latest
```

Each build is also tagged by its commit, so a deployment can pin to one
and roll back to it. The compose file in this repository builds from
source instead, which is what you want while developing:

```bash
docker compose up --build
```

That starts Maestro and Postgres together, migrates the schema on first
boot, and creates the account named by `FIRST_ADMIN_EMAIL` /
`FIRST_ADMIN_PASSWORD` as an instance admin. Open
**http://localhost:8090** and sign in with it. (The container listens on
8080; the compose file publishes it on 8090 by default, so it does not
collide with anything else you have running. `MAESTRO_HOST_PORT`
changes that.)

### Configuration

Every setting is an environment variable on the `maestro` service.

| Variable | Default | What it does |
| :--- | :--- | :--- |
| `DATABASE_URL` | *(required)* | Postgres connection string. |
| `MAESTRO_ADDR` | `:8080` | Address the server listens on, inside the container. |
| `REGISTRATION_MODE` | `invite_only` | `invite_only`, or `domain_open` to let anyone with an allowed address sign themselves up. |
| `ALLOWED_EMAIL_DOMAINS` | *(empty)* | Comma-separated list. Required by `domain_open`. |
| `SESSION_TTL` | `720h` | How long a sign-in lasts. |
| `INVITE_TTL` | `336h` | How long an invitation link stays usable. |
| `FIRST_ADMIN_EMAIL` | *(empty)* | The account seeded, and re-promoted to admin, at boot. |
| `FIRST_ADMIN_PASSWORD` | *(empty)* | Its password, on a brand-new instance. |
| `FIRST_ADMIN_PASSWORD_RESET` | `false` | One-shot break-glass: see *Recovering an admin*. |
| `TRUSTED_PROXY_COUNT` | `0` | How many reverse proxies sit in front, for client addresses. |

### Adding people

Sign in as an admin and open **Administration** from the menu under your
name. Create an invitation for an address, and Maestro answers with a
link.

**Nothing is sent anywhere.** This instance delivers no email, by
design, so you pass that link on yourself, and it is shown once. The
person opens it, sets a password, and has an account — and nothing else:
an account-only invitation grants no game. A game's owner invites
somebody into a game, at a role, from that game.

The same screen makes somebody an administrator, or takes it away, by
the address they sign in with. An admin may demote another admin, or
themselves, as long as one remains: the instance refuses to be left with
none.

### Passwords

Anybody signed in changes their own password from **Your account**. It
requires the current one.

**There is no password reset.** No email is ever sent, so there is
nothing to send a reset to: an administrator invites somebody again
instead. Changing a password does not revoke API tokens, which have no
expiry and are revoked only explicitly, so anybody rotating a password
on suspicion should also check that game's tokens.

### Recovering an admin

With no reset flow, a forgotten admin password is recovered by an
operator restarting the process. Two things can happen at boot, and both
need `FIRST_ADMIN_EMAIL` and `FIRST_ADMIN_PASSWORD` set:

- **The admin flag** is restored on every restart, whether or not the
  account ever had it.
- **The password** is overwritten **only** when
  `FIRST_ADMIN_PASSWORD_RESET=true` is also set. Without it,
  `FIRST_ADMIN_PASSWORD` only ever seeds a brand-new instance, which is
  why an admin who changes their own password keeps it across restarts.

So a recovery is one deliberate boot:

```bash
FIRST_ADMIN_EMAIL=admin@example.test \
FIRST_ADMIN_PASSWORD=the-new-password \
FIRST_ADMIN_PASSWORD_RESET=true \
  docker compose up -d
```

It revokes every session for that account in the same transaction that
writes the new password, and logs what it did at `WARN`. **Unset
`FIRST_ADMIN_PASSWORD_RESET` again afterwards**: left set, it is a
standing credential for anyone who can read that environment.

## Backups

Off unless you ask for them. Name a directory and the instance writes
one dump a night:

```yaml
environment:
  MAESTRO_BACKUP_DIR: /backups
  MAESTRO_BACKUP_AT: "03:00"        # local time, HH:MM, default 03:00
  MAESTRO_BACKUP_KEEP_DAYS: "7"     # default 7
  TZ: Europe/Madrid                 # or the hour above is UTC
volumes:
  - ./backups:/backups
```

Each file is `pg_dump --format=custom`, named
`maestro-YYYY-MM-DD-HHMM.dump`, restored with `pg_restore`:

```bash
pg_restore --clean --if-exists --no-owner   --dbname "postgres://maestro:maestro@localhost:5432/maestro"   maestro-2026-09-21-0300.dump
```

Restoring a dump written by the shipped image into an **older** server
prints one ignorable error — `unrecognized configuration parameter
"transaction_timeout"` — because the image carries the Postgres 17
client and that setting does not exist before 17. The restore itself
completes; `pg_restore` says so as `errors ignored on restore: 1`.

**The hour is local to the container, and a container has no local time
unless you give it one.** The image carries `tzdata`, so `TZ` works:
without it you get UTC, which is a dump at the right minute of the
wrong hour. Set `TZ` beside the backup variables if the hour matters to
you.

A dump is the whole instance in one file — every game's content, the
prose, the accounts and their addresses, the API tokens — so it is
written `0600` inside a `0700` directory, and an existing directory is
tightened on start-up. When the mount will not allow that, the instance
logs a warning and carries on: widening the mode is never the answer,
and granting a backup agent access by owner or by a group the host
controls is.

The database password reaches `pg_dump` through the environment, not as
a command-line argument, so it does not appear in `ps` on the host. The
dump is written to a `.tmp` name and renamed, so an agent copying the
directory never picks up a file that is still being written. A dump
interrupted by a restart leaves a `.tmp` behind; those are swept once
they are a day old. Retention only ever removes files this instance
wrote — anything else in that directory is yours and is left alone.

**What is still your job: getting the dumps off this machine.** A
backup on the same disk as the database survives a bad migration and
nothing else.

The image runs as UID **and** GID `65532`, both pinned, so a host that
grants access by group can rely on the number across rebuilds.

## Building it

Go 1.27 and a Postgres to test against. No Node, no bundler: the front
end is vanilla CSS and ES modules, served from the binary.

```bash
make build        # bin/maestro
make run          # build, then run it
make check        # the full gate: gofmt, vet, lint, sqlc diff, skill guards, tests
```

`make check` is what CI runs on every push, and it is the bar for a
commit.

### Tests

The test suite is integration-first: it runs against a real Postgres,
and **every test that needs one skips silently when `TEST_DATABASE_URL`
is unset**, with `go test` still printing `ok`. Set it:

```bash
docker run -d --name maestro-test-pg -e POSTGRES_PASSWORD=postgres \
  -p 55432:5432 postgres:16

export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable"
go test ./...
```

Each test gets its own database, created and dropped around it, so runs
do not interfere. `internal/web` is the slow package, at a couple of
minutes; a single test is seconds, so run the one you are working on.

A run that is interrupted never reaches its own cleanup, so its database
stays behind. The next run sweeps anything over an hour old, and
`make clean-test-dbs` does it now. `make clean-docker` adds the images
and the build cache that repeated `make dev` rebuilds leave behind.

### A local instance while developing

```bash
make dev          # build the image and start it, on http://localhost:8090
make demo         # write a game into it that the screens can be seen on
make dev-logs     # follow the server's log
make dev-psql     # a psql shell on its database
make dev-down     # stop it, keeping the data volume
```

`make dev` rebuilds, so it is also how a code change reaches the
browser. The data lives in a named volume and survives `dev-down`.

**`make demo` is worth running the first time.** A fresh instance is
empty, and several of Maestro's screens only exist when something is in
them: the analysis reports need a design with a loop and a dead end in
it, the catalogue's ordering and paging need a type with more rows than
one page, and the images list needs an image. The demo writes one game
with all of that in it, through the same domain code the API uses.

### Other targets

```bash
make docs         # build the documentation site into site/
make tools        # install sqlc and the linter
make sqlc         # regenerate the database layer from internal/db/queries
make sqlc-check   # fail if the generated code is stale
make skill-check  # the agent bundle's own guards
make clean-test-dbs  # drop test databases interrupted runs left behind
make clean-docker    # those, plus Maestro's stale images and the build cache
```

Queries are written in SQL and compiled by [sqlc](https://sqlc.dev); the
generated code is committed, and `make check` fails if it drifts from
the `.sql` files.

## Known limitations

Said plainly, rather than left to be discovered:

- **The query builder holds the half of the language a sentence can
  hold.** Start from a kind of thing, narrow it, follow one chain of
  connections, and say how to draw it. A question with two branches, a
  depth range or a parameter is written as a document, which is what an
  agent is for — and the screen says so under the clause stack rather
  than leaving it to be found out. The builder also never edits a stored
  query: it opens one only if that document round-trips through it
  unchanged.
- **One process only.** Events fan out from an in-memory hub rather than
  Postgres `LISTEN`/`NOTIFY`, and the rate limiters are in-process, so a
  second replica has its own subscribers and its own budgets. A client
  sees only events published while its process has been running: there
  is no durable log and no catch-up on reconnect.
- **Backups are opt-in and off by default.** Set `MAESTRO_BACKUP_DIR`
  and the instance writes one `pg_dump --format=custom` a night; leave
  it unset, as the shipped compose file does, and nothing is written
  and nothing changes. See "Backups" below for the mode of the files,
  the retention and what is still yours to do (copying them somewhere
  that is not this machine).
- **argon2id cost is fixed in code** (`Time=3`, `Memory=64MiB`,
  `Threads=2`), not configurable.
- **Nobody has verified that the skill bundle teaches.** Its guards
  prove it agrees with the server, and say nothing about whether an
  agent reading it can actually start.

## Roadmap

- [x] **Core.** Server, Postgres, identity, MCP and REST surfaces.
- [x] **Metamodel.** The four primitives, field schemas, validation.
- [x] **Markdown.** Versioned prose, linked to entities.
- [x] **Views.** The query language, saved views, layouts, coordinates.
- [x] **Interface.** Maestro's own look, and the renderer catalogue.
- [x] **Analysis.** Cycles, unreachable content, orphans, routes.
- [x] **Skills.** The agent bundle and genre templates.

## License

MIT. See [license.md](license.md).
