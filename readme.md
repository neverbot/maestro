# Maestro

Open-source, self-hosted workspace where **game designers and their AI
agents co-design the content of a video game**: characters, places,
missions, progression and narrative.

Maestro is not a project tracker — no backlog, no kanban, no sprints.
And it never touches a running game: no live instances, no real
players, no telemetry. Every row in it is a design-time artefact.

> **Status: design phase.** There is no code yet. The design lives in
> [`docs/superpowers/specs/`](docs/superpowers/specs/).

## The idea

Maestro ships **no built-in game concepts**. There is no quest table,
no zone table, no character table. Each game declares its own
vocabulary out of four primitives:

| Primitive      | Meaning                                        |
| -------------- | ---------------------------------------------- |
| `EntityType`   | a kind of thing, with a field schema           |
| `Entity`       | an instance of an entity type                  |
| `RelationType` | a kind of directed edge, with its own schema   |
| `Relation`     | an instance of a relation type between two     |

An MMORPG declares `Class`, `Zone`, `Dungeon`, `Quest`, `Talent`, and
edges like `connects_to`, `takes_place_in`, `requires`, `rewards`.

A racing career game declares `Driver`, `Car`, `Circuit`, `Race`,
`Championship`, `Licence`, and edges like `unlocks` and `contains`.

A metroidvania declares `Room`, `Ability`, `Boss` — with the edge
itself carrying the condition (`requires_ability`) that decides whether
a door can be crossed.

Maestro understands none of those words. It understands typed entities
and typed directed edges. That is the whole point: a genre it has never
seen must fit without a code change.

On top of that graph it gives designers:

- **Catalogues** of everything a game contains, by type.
- **Views** — saved diagrams defined as a query plus a layout, which an
  agent can create: a world map with real coordinates and a background
  image, a layered progression tree, a table, a timeline.
- **Analysis** — prerequisite cycles, unreachable content, orphans, and
  saved routes ("Mage levelling 1-20") used to validate a design.
- **Prose** — versioned markdown for lore and mission scripts, linked
  to the entities it describes.

Humans work in a web UI. Agents drive the same data over **MCP**, and
learn the metamodel from a skill bundle with worked examples per genre.

## Design

- [Core and metamodel](docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md)
  — the first sub-project: server, identity, the four primitives, MCP
  surface. Includes the full roadmap.

## Related

Maestro's architecture follows [Nottario](https://github.com/neverbot/nottario),
a self-hosted coordinator for developers and their agents — same
deployment shape (one Go binary, Postgres, MCP over HTTP+SSE), a
different domain and a different look.

## License

MIT — see [license.md](license.md).
