WITH RECURSIVE s0 (id, set_name) AS (
    SELECT e.id, $3::text
    FROM entities e
    WHERE e.project_id = $1
      AND e.entity_type_id = $2
      AND e.invalid = false
      AND true
),
node_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT e.id, e.key, e.name, et.key, s0.set_name, $4::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $5::integer
    FROM s0
    JOIN entities e ON e.id = s0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank)
        ORDER BY all_rows.id, all_rows.rank
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $6
),
edge_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $7::integer
    FROM relations r
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
    WHERE r.project_id = $1
      AND r.relation_type_id = ANY($8::uuid[])
      AND (r.source_id IN (SELECT id FROM s0) AND r.target_id IN (SELECT id FROM s0))
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank)
        ORDER BY all_rows.id, all_rows.rank
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $9
)
SELECT 'node' AS kind, n.* FROM node_rows n
UNION ALL
SELECT 'edge' AS kind, e.* FROM edge_rows e
ORDER BY 1, 11, 2
