<h1>Maestro</h1>

**A workspace where game designers and their AI agents build the design
of a game together: characters, places, missions, progression, story.**

[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](license.md)
![Status: design phase](https://img.shields.io/badge/status-design%20phase-orange)

Self-hosted. Open source. It holds the *design* of a game, never a
running one: no live instances, no real players, no telemetry.

> **There is no code yet.** The design lives in
> [`docs/superpowers/specs/`](docs/superpowers/specs/). Watch the repo
> if you want to see it get built.

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

<table>
<tr><th align="left">Genre</th><th align="left">Declares</th><th align="left">Connected by</th></tr>
<tr>
  <td><b>MMORPG</b></td>
  <td><code>Class</code> <code>Zone</code> <code>Dungeon</code> <code>Quest</code> <code>Talent</code></td>
  <td><code>connects_to</code> <code>takes_place_in</code> <code>requires</code> <code>rewards</code></td>
</tr>
<tr>
  <td><b>Racing career</b></td>
  <td><code>Driver</code> <code>Car</code> <code>Circuit</code> <code>Race</code> <code>Licence</code></td>
  <td><code>unlocks</code> <code>contains</code> <code>requires</code></td>
</tr>
<tr>
  <td><b>Metroidvania</b></td>
  <td><code>Room</code> <code>Ability</code> <code>Boss</code> <code>Item</code></td>
  <td><code>connects_to</code>, carrying its own <code>requires_ability</code></td>
</tr>
</table>

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

## Roadmap

- [ ] **Core.** Server, Postgres, identity, MCP and REST surfaces.
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
