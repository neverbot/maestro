# Maestro

**A workspace where game designers and their AI agents build the design
of a game together: characters, places, missions, progression, story.**

[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](license.md)
![Status: early development](https://img.shields.io/badge/status-early%20development-orange)

Self-hosted. Open source. It holds the *design* of a game, never a
running one: no live instances, no real players, no telemetry.

> **Early days.** The server runs — accounts, games, agent tokens, the
> MCP surface and live updates are in place — but the game-design domain
> itself is not built yet, so there is nothing to model with. See
> [Running it](#running-it) to start an instance, and
> [`docs/superpowers/specs/`](docs/superpowers/specs/) for the design.

## Maestro knows nothing about games

No quest table. No zone table. No character table. Every game brings its
own vocabulary, built from four primitives:

| Primitive      | What it is                                    |
| :------------- | :-------------------------------------------- |
| `EntityType`   | a kind of thing, with a field schema           |
| `Entity`       | one instance of that kind                      |
| `RelationType` | a kind of directed edge, with its own schema   |
| `Relation`     | one edge, from one entity to another           |

That is the whole model. A genre nobody anticipated has to fit without a
code change, so nothing about any genre is baked in.

| Genre              | Declares                                                        | Connected by                                                                  |
| :----------------- | :-------------------------------------------------------------- | :---------------------------------------------------------------------------- |
| **MMORPG**         | `Class` `Zone` `Dungeon` `Quest` `Talent`                        | `connects_to` `takes_place_in` `requires` `rewards`                            |
| **Racing career**  | `Driver` `Car` `Circuit` `Race` `Licence`                        | `unlocks` `contains` `requires`                                                |
| **Metroidvania**   | `Room` `Ability` `Boss` `Item`                                   | `connects_to`, carrying its own `requires_ability`                             |

That last one is the point of typed edges: whether a door can be crossed
belongs to the door, not to either room.

## What you get on top of the graph

**Catalogues.** Every class, every circuit, every mission the game
contains, listed by type, filterable, searchable.

**Views.** A saved diagram is a query plus a layout, and an agent can
write one. A world map with real coordinates over a background image. A
layered progression tree. A table. A timeline. Ask for *the quests a
Mage can reach between level 20 and 30, coloured by zone*, and that is a
view, not a feature request.

**Analysis.** Prerequisite cycles. Content no player can ever reach.
Orphans. Saved routes such as *Mage levelling 1-20*, used to prove a
progression actually holds together. This is the reading nobody can do
by eye across four hundred missions.

**Prose.** Lore and mission scripts as versioned markdown, attached to
the entities they describe.

## Two ways in

Humans work in a web UI. Agents drive the same data over **MCP**, and
learn the metamodel from a skill bundle carrying worked examples per
genre, so an agent arrives knowing how to declare types and seed a few
hundred entities without being told twice.

## Running it

```bash
docker compose up --build
```

Starts Maestro and Postgres together (`compose.yml`). On first boot it
migrates the schema and, if `FIRST_ADMIN_EMAIL`/`FIRST_ADMIN_PASSWORD`
are set, creates that account as an instance admin — log in with it at
`http://localhost:8080`.

For local development without a container: `make build && make run`
(binary in `bin/maestro`, same environment variables as the compose
service). `make check` runs the full commit gate — formatting, `go vet`,
the linter, `sqlc diff`, and the test suite — the same one CI runs.

**Admitting a second person.** `invite_only` is the default
`REGISTRATION_MODE`, so nobody else can sign up on their own. The admin
account mints an invite from the terminal — no UI for this yet, no
`psql` either:

```bash
curl -s -b cookies.txt -X POST http://localhost:8080/api/invites \
  -H 'Content-Type: application/json' \
  -d '{"email":"designer@studio.com"}'
```

(`-b cookies.txt` reuses the session cookie saved from `POST
/api/auth/login`.) The response carries `redeem_path`; paste it after
the instance's own address and hand the link to whoever it's for —
`http://localhost:8080/login#invite=<token>`. They open it, set a
password, and they're in — with an account, and nothing else: an
account-only invite grants no game. A game's own owner can invite
someone straight into that game instead, at a role, from
`POST /api/games/{game}/invites` with `{"email":"...","role":"editor"}`,
the same way.

**Changing a password, and a second admin.** Anyone signed in can rotate
their own password — it requires the current one, and it logs every
*other* session out (not the one that made the request):

```bash
curl -s -b cookies.txt -X PATCH http://localhost:8080/api/me/password \
  -H 'Content-Type: application/json' \
  -d '{"current_password":"old-password","new_password":"a-new-password"}'
```

Rotating a password this way is the only way to get an attacker who
merely stole a session cookie off the account: there is no
forgot-my-password flow anywhere in this product — no email is ever
sent, by design — and the one reset that does exist (below) is an
operator restarting the process, not something a signed-in user or a
locked-out one can reach. It is not a
way to be sure a thief is locked out entirely: an API token minted
before the rotation keeps working afterwards (tokens have no expiry,
only explicit revocation), so anyone rotating a password on suspicion
should also check that game's token list (`GET
/api/games/{game}/tokens`) for anything unrecognised. An existing
instance admin can also promote a colleague to admin, by email — the
bootstrap account created at first boot is no longer the only one that
can ever mint an account-only invite:

```bash
curl -s -b cookies.txt -X PATCH http://localhost:8080/api/admins \
  -H 'Content-Type: application/json' \
  -d '{"email":"colleague@studio.com","is_admin":true}'
```

An admin may demote another admin, or themselves, as long as at least
one remains — the instance refuses to ever be left with zero.

**Recovering a locked-out admin.** With no password reset flow for
users, a forgotten or leaked admin password is recovered by an operator
restarting the process. Two separate things happen at boot when
`FIRST_ADMIN_EMAIL` names an account that already exists:

- **Restoring the admin flag** happens on any restart, with no extra
  configuration. An account that lost `is_admin` gets it back.
- **Resetting that account's password** to `FIRST_ADMIN_PASSWORD`
  happens **only** when `FIRST_ADMIN_PASSWORD_RESET=true` is also set.
  Without it, `FIRST_ADMIN_PASSWORD` is only ever a seed for a brand-new
  instance and can never overwrite an existing account's password. That
  is why `compose.yml` can leave `FIRST_ADMIN_EMAIL`/`FIRST_ADMIN_PASSWORD`
  set permanently, and why an admin who rotates their own password keeps
  it across every ordinary restart.

So a recovery is one deliberate boot:

```bash
FIRST_ADMIN_EMAIL=admin@studio.com \
FIRST_ADMIN_PASSWORD=the-new-password \
FIRST_ADMIN_PASSWORD_RESET=true \
  docker compose up -d
```

**The reset revokes every session for that account**, on every device,
in the same transaction that writes the new hash — the same guarantee
an ordinary password change gives. It does not revoke API tokens; those
have no expiry and are revoked only explicitly, so check that game's
token list afterwards. Both the promotion and the reset are logged at
`WARN` with the account's user id, so a restart that changed either is
visible in the process log.

**Unset `FIRST_ADMIN_PASSWORD_RESET` again once you have logged in.**
Left set, it is a standing break-glass credential: anyone who later
learns `FIRST_ADMIN_PASSWORD`, or can write the environment it lives in,
can reset that account on the next restart. Access to the process
environment already implies database access, so this grants nothing new
— it just turns a break-glass `psql` session into a documented restart —
but there is no reason to leave the door open after walking through it.
Note also that with the opt-in set, a `FIRST_ADMIN_PASSWORD` shorter
than twelve characters aborts start-up rather than half-applying, since
the reset goes through the same validation every other password change
in this product does.

**Clicking an invite while already signed in.** A project-bound invite
redeemed by a browser tab that is already logged in grants membership to
that same account — it never creates a second one. An email bound to a
different account than the one currently logged in is refused, the same
as an unknown token.

## Known limitations

- **Single-process SSE, single-process rate limiting.** `/events` fans
  events out from an in-memory hub inside one process, not Postgres
  `LISTEN`/`NOTIFY`. A client only ever sees events published while its
  own process has been running — there is no durable log behind it and
  no catch-up on reconnect — and running more than one replica splits
  subscribers across hubs that never talk to each other. The login,
  invite-redemption and password-change rate limiters are in-process for
  the same reason: N replicas means N independent budgets, not one
  shared across the instance.
- **The container image is not published anywhere.** CI builds it and
  smoke-tests it against a real Postgres on every push to `master`, but
  nothing pushes it to a registry. Building from this repository
  (`docker compose up --build`, or `docker build .`) is currently the
  only way to run it.
- **argon2id cost parameters are fixed in code, not configurable.**
  `Time=3`, `Memory=64MiB`, `Threads=2` (`internal/config/config.go`).
  Changing them means changing the default and rebuilding, not setting
  an environment variable.
- **No backups, no documentation site.** Scheduled `pg_dump` backups and
  a public documentation site are design-stage only; both were moved to
  later sub-projects, and nothing in this repository runs either yet.

## Roadmap

- [x] **Core.** Server, Postgres, identity, MCP and REST surfaces.
- [ ] **Metamodel.** The four primitives, field schemas, validation.
- [ ] **Markdown.** Versioned prose, linked to entities.
- [ ] **Views.** The query language, saved views, layouts, coordinates.
- [ ] **Interface.** Maestro's own look, and the renderer catalogue.
- [ ] **Analysis.** Cycles, unreachable content, orphans, routes.
- [ ] **Skills.** The agent bundle and genre templates.

Design documents, one per sub-project as they land:

- [Core and metamodel](docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md)

## License

MIT. See [license.md](license.md).
