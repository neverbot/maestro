WITH RECURSIVE s0 (id, set_name, depth) AS (
    SELECT e.id, $4::text, 0
    FROM entities e
    WHERE e.project_id = $1
      AND e.entity_type_id = $2
      AND e.invalid = false
      AND (lower(e.key) = $3)
),
t0 (id, set_name, from_id, via_relation, depth) AS (
    SELECT r.source_id, $10::text, near.id, r.id, near.depth + 1
    FROM s0 near
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($11::uuid[])
     AND r.invalid = false
     AND r.target_id = near.id
     AND true
    JOIN entities far
      ON far.id = (r.source_id)
     AND far.project_id = $1
     AND far.invalid = false
     AND far.entity_type_id = ANY($9::uuid[])
    WHERE ((jsonb_typeof((far.fields -> $5)) = 'number' AND (far.fields ->> $5)::numeric >= $6) AND (jsonb_typeof((far.fields -> $7)) = 'number' AND (far.fields ->> $7)::numeric <= $8))
),
node_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth, attrs, ambiguous) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT e.id, e.key, e.name, et.key, s0.set_name, $17::text,
           NULL::uuid, NULL::uuid, jsonb_strip_nulls(jsonb_build_object($16::text, e.fields -> $16)), $18::integer, s0.depth, jsonb_strip_nulls(jsonb_build_object($12::text, to_jsonb(e.name), $13::text, p1.value)), (COALESCE(p1.matches, 0) > 1)
    FROM s0
    JOIN entities e ON e.id = s0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
    LEFT JOIN LATERAL (
        SELECT to_jsonb(far.name) AS value, count(*) OVER () AS matches
        FROM relations rel
        JOIN entities far ON far.id = rel.target_id
                         AND far.project_id = $1
                         AND far.entity_type_id = $14
                         AND far.invalid = false
        WHERE rel.project_id = $1
          AND rel.relation_type_id = $15
          AND rel.invalid = false
          AND rel.source_id = e.id
        GROUP BY far.id, far.name
        ORDER BY far.name, far.id
        LIMIT 1
    ) AS p1 ON true
  UNION
    SELECT e.id, e.key, e.name, et.key, t0.set_name, $19::text,
           NULL::uuid, NULL::uuid, jsonb_strip_nulls(jsonb_build_object($16::text, e.fields -> $16)), $20::integer, t0.depth, jsonb_strip_nulls(jsonb_build_object($12::text, to_jsonb(e.name), $13::text, p1.value)), (COALESCE(p1.matches, 0) > 1)
    FROM t0
    JOIN entities e ON e.id = t0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
    LEFT JOIN LATERAL (
        SELECT to_jsonb(far.name) AS value, count(*) OVER () AS matches
        FROM relations rel
        JOIN entities far ON far.id = rel.target_id
                         AND far.project_id = $1
                         AND far.entity_type_id = $14
                         AND far.invalid = false
        WHERE rel.project_id = $1
          AND rel.relation_type_id = $15
          AND rel.invalid = false
          AND rel.source_id = e.id
        GROUP BY far.id, far.name
        ORDER BY far.name, far.id
        LIMIT 1
    ) AS p1 ON true
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth, attrs, ambiguous)
        ORDER BY all_rows.id, all_rows.rank, all_rows.depth
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $21
),
edge_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth, attrs, ambiguous) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $22::integer, t0.depth, NULL::jsonb, NULL::boolean
    FROM t0
    JOIN relations r ON r.id = t0.via_relation AND r.project_id = $1
      AND r.invalid = false
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
  UNION
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $23::integer, NULL::integer, NULL::jsonb, NULL::boolean
    FROM relations r
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
    WHERE r.project_id = $1
      AND r.relation_type_id = ANY($24::uuid[])
      AND r.invalid = false
      AND (r.source_id IN (SELECT id FROM t0) AND r.target_id IN (SELECT id FROM t0))
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth, attrs, ambiguous)
        ORDER BY all_rows.id, all_rows.rank, all_rows.depth
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $25
)
SELECT 'node' AS kind, n.* FROM node_rows n
UNION ALL
SELECT 'edge' AS kind, e.* FROM edge_rows e
ORDER BY 1, 11, 2
