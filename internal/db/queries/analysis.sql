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
-- The adjacency arms are symmetric types, listed in both directions and
-- marked gate = false: a symmetric edge joins two entities without
-- ordering them, so it may admit under `any` and may never be required
-- under `all`.
--
-- exclude_invalid is a parameter and not a constant because this
-- package's default is to **follow** invalid edges -- see
-- analysis.Params for the argument, which is that `invalid` describes a
-- row's fields and this question reads only its endpoints.
-- name: ListNormalisedInEdges :many
SELECT dependent, needed, gate
FROM (
        SELECT r.target_id AS dependent, r.source_id AS needed,
               true AS gate, r.invalid AS invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@forward_gates::uuid[])
    UNION ALL
        SELECT r.source_id, r.target_id, true, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@reverse_gates::uuid[])
    UNION ALL
        SELECT r.target_id, r.source_id, false, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@adjacency::uuid[])
    UNION ALL
        SELECT r.source_id, r.target_id, false, r.invalid
        FROM relations r
        WHERE r.project_id = @project_id
          AND r.relation_type_id = ANY(@adjacency::uuid[])
) AS edges
WHERE dependent = ANY(@dependents::uuid[])
  AND (NOT @exclude_invalid::boolean OR NOT invalid);
