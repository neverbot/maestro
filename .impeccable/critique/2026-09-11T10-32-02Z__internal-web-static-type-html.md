---
target: type
total_score: 21
p0_count: 2
p1_count: 2
timestamp: 2026-09-11T10-32-02Z
slug: internal-web-static-type-html
---
# Critique: the catalogue — types, one type, one entity

Measured in Firefox at 1440 and 500, on a 105-entity type and a
1000-entity type. Recorded after the fact from the review's own report.

## Design health: 21/40

## Priority issues, as found

- **P0** Every row was its own grid, and sibling grids share nothing: the
  column header sat 18.5px off the values it named, `faction`'s box
  overlapped the `x` column's, and two rows with different key lengths
  did not line up with each other.
- **P0** A 62-character name collapsed the name track to 0px at a 420px
  list and set the label one word per line: a measured 261px row.
- **P1** Search reported "50 matches" for a query matching a thousand,
  and a search that found nothing rendered the listing's empty state,
  "Nothing of this type yet", under a heading saying 105 entities.
- **P1** A search result dropped the columns while keeping their header.
- **P2** The Named Absence Rule was dropped twice: `""` rendered
  identically to absent, and the absent word used the field's key rather
  than its label.

## What was working

`CATALOGUE_NOTE`; the 500px catalogue; row heights of exactly {36, 28}
at every width.

## Fixed in

`ae58cfe`, `2c3d3ff`, `05c2f73`.
