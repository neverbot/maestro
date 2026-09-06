-- Statements for internal/analysis. Every one of them filters on the
-- resolved project id, in SQL and never in Go, which is this
-- repository's isolation rule and not this file's own decision.
--
-- The recursive walks the three analyses run are **not** here: they are
-- emitted by internal/graph at run time and therefore cannot go through
-- sqlc, exactly as internal/views/compile.go's statements cannot. What
-- is here is everything static.

-- ListRouteStepsByRouteKey reads a route's steps in order, addressed by
-- the route's key and this game's id.
--
-- entity_id is nullable: 0013_analysis.sql tombstones a step whose
-- entity was deleted with ON DELETE SET NULL rather than removing it, so
-- a route visibly keeps a step it can no longer resolve. A caller must
-- have an opinion about the null; analysis.routeSeeds contributes no
-- seed for one and still names it in the seed report.
-- name: ListRouteStepsByRouteKey :many
SELECT s.position, s.entity_id, s.entity_type_key, s.entity_key
FROM route_steps s
JOIN routes rt ON rt.id = s.route_id AND rt.project_id = s.project_id
WHERE s.project_id = @project_id AND lower(rt.key) = lower(@key)
ORDER BY s.position;

-- RouteExists separates "no such route" from "a route with no steps",
-- which ListRouteStepsByRouteKey answers identically and which are two
-- different refusals.
-- name: RouteExists :one
SELECT EXISTS (
    SELECT 1 FROM routes WHERE project_id = @project_id AND lower(key) = lower(@key)
) AS route_exists;

-- ListNormalisedInEdges is the in-neighbourhood of a set of entities in
-- the engine's one normalised direction, needed → dependent.
--
-- The two gating arms are the reversal itself: a forward type's edge
-- gates its target, a reverse (`prerequisite_of`) type's edge gates its
-- source. After this statement nothing downstream knows which trait an
-- edge carried, which is the whole point of normalising.
--
-- Containment has an arm of its own rather than riding in with the
-- forward gates, because the unreachable analysis owes a reason and
-- "the only way in is through a container nobody can reach" is a
-- different sentence, with a different fix, from "every route to it is
-- blocked". Everything else treats a container as a gate.
--
-- The adjacency arms are symmetric types, listed in both directions and
-- marked `adjacency`: a symmetric edge joins two entities without
-- ordering them, so it may admit under `any` and may never be required
-- under `all`.
--
-- exclude_invalid is a parameter and not a constant because this
-- package's default is to **follow** invalid edges -- see
-- analysis.Params for the argument, which is that `invalid` describes a
-- row's fields and this question reads only its endpoints.
-- name: ListNormalisedInEdges :many
SELECT dependent, needed, kind
FROM (
        SELECT r.target_id AS dependent, r.source_id AS needed,
               'gate'::text AS kind, r.invalid AS invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@forward_gates::uuid[])
    UNION ALL
        SELECT r.source_id, r.target_id, 'gate'::text, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@reverse_gates::uuid[])
    UNION ALL
        SELECT r.target_id, r.source_id, 'containment'::text, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@containment::uuid[])
    UNION ALL
        SELECT r.target_id, r.source_id, 'adjacency'::text, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@adjacency::uuid[])
    UNION ALL
        SELECT r.source_id, r.target_id, 'adjacency'::text, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@adjacency::uuid[])
) AS edges
WHERE dependent = ANY(@dependents::uuid[])
  AND (NOT @exclude_invalid::boolean OR NOT invalid);

-- ListRelationsByIDs reads the edges of a cycle back, so a finding can
-- name the relation *type* of every one of them -- which is the field
-- Task 4's widening of internal/graph exists for, because the first
-- thing a designer does with a reported loop is ask whether the type
-- should have been declared gating at all.
--
-- The ids come out of a walk this game's own project id already bounded,
-- so the project filter here is redundant today. It is written anyway,
-- for the reason every statement in this file carries one: isolation is
-- a property of the statement and not of the caller that happens to feed
-- it, and the next caller of this statement will not be that walk.
-- name: ListRelationsByIDs :many
SELECT r.id, r.relation_type_id, r.source_id, r.target_id, r.invalid
FROM relations r
WHERE r.project_id = @project_id AND r.id = ANY(@ids::uuid[]);

-- CountConsideredEntities is the denominator of an orphan report: how
-- many entities this run looked at.
--
-- It exists because an empty findings list means one of two entirely
-- different things -- nothing is orphaned, or nothing was considered --
-- and those are the same JSON without a count beside them. Ignored
-- entity types are subtracted from the id list before it gets here, so a
-- total cannot count what the report excludes.
-- name: CountConsideredEntities :one
SELECT count(*) AS considered
FROM entities e
WHERE e.project_id = @project_id AND e.entity_type_id = ANY(@entity_types::uuid[]);

-- ListOrphansPage is the orphan analysis, and it is **an aggregate with
-- no recursion anywhere in it**.
--
-- That is stated because three analyses of runtime-built recursive SQL
-- in a row make this one look like it should be a fourth. It is not: an
-- orphan is an entity with no edges, which is a degree, and a degree is
-- a count. So this is static SQL and goes through sqlc like every other
-- statement in this repository.
--
-- **It reads `relations` regardless of `invalid`**, which is the
-- decision analysis.Params argues in full and the place it is most
-- likely to be forgotten: an entity whose only edge is flagged invalid
-- **is not an orphan**, and calling it one would send a designer to
-- delete content that has edges. `invalid` describes whether a row's
-- own fields still fit its type's field_schema; it says nothing about
-- the row's endpoints, and endpoints are the only thing this statement
-- reads. Adding `AND r.invalid = false` below is the one-line "tidy"
-- TestAnEntityWhoseOnlyEdgeIsInvalidIsNotAnOrphan exists to make loud.
--
-- ignored_relation_types is the annotation types, and it is a
-- **subtraction** rather than a list of types to count, deliberately: an
-- orphan is an entity nothing points at, so every type counts unless a
-- designer has said in the metamodel that this one is decoration. An
-- empty array excludes nothing, which is what a game that declared no
-- traits gets -- and which is a perfectly meaningful input rather than a
-- refusal, unlike the other three analyses.
--
-- The keyset is (entity type key, folded entity key, id), which is a
-- total order, so a cursor can neither skip nor repeat a row. Keys are
-- folded because every key in Maestro is matched without regard to case,
-- and a sort that disagreed with the fold would order two spellings of
-- one key inconsistently between pages.
-- name: ListOrphansPage :many
SELECT e.id, e.key, e.name, et.key AS entity_type_key,
       ind.n::bigint AS in_degree, outd.n::bigint AS out_degree
FROM entities e
JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = @project_id
LEFT JOIN LATERAL (
    SELECT count(*) AS n FROM relations r
    WHERE r.project_id = @project_id AND r.target_id = e.id
      AND NOT (r.relation_type_id = ANY(@ignored_relation_types::uuid[]))
) ind ON true
LEFT JOIN LATERAL (
    SELECT count(*) AS n FROM relations r
    WHERE r.project_id = @project_id AND r.source_id = e.id
      AND NOT (r.relation_type_id = ANY(@ignored_relation_types::uuid[]))
) outd ON true
WHERE e.project_id = @project_id
  AND e.entity_type_id = ANY(@entity_types::uuid[])
  AND CASE @mode::text
        WHEN 'sink'   THEN ind.n > 0 AND outd.n = 0
        WHEN 'source' THEN ind.n = 0 AND outd.n > 0
        ELSE ind.n = 0 AND outd.n = 0
      END
  AND (sqlc.narg('after_id')::uuid IS NULL
       OR (et.key, lower(e.key), e.id) > (sqlc.narg('after_entity_type')::text,
                                          sqlc.narg('after_key')::text,
                                          sqlc.narg('after_id')::uuid))
ORDER BY et.key, lower(e.key), e.id
LIMIT sqlc.arg('limit')::int;
