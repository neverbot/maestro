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
`http://localhost:8080`. `compose.yml`'s own `SESSION_KEY` is a
local-dev-only placeholder; generate a real one with
`openssl rand -base64 32` before running this anywhere but a laptop.

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
