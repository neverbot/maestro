WITH RECURSIVE s0 (id, set_name) AS (
    SELECT e.id, $4::text
    FROM entities e
    WHERE e.project_id = $1
      AND e.entity_type_id = $2
      AND e.invalid = false
      AND (lower(e.key) = $3)
),
t0 (id, set_name, from_id, via_relation) AS (
    SELECT r.source_id, $6::text, near.id, r.id
    FROM s0 near
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($7::uuid[])
     AND r.target_id = near.id
     AND true
    JOIN entities far
      ON far.id = (r.source_id)
     AND far.project_id = $1
     AND far.invalid = false
     AND far.entity_type_id = ANY($5::uuid[])
    WHERE true
),
t1 (id, set_name, from_id, via_relation) AS (
    SELECT r.target_id, $9::text, near.id, r.id
    FROM t0 near
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($10::uuid[])
     AND r.source_id = near.id
     AND true
    JOIN entities far
      ON far.id = (r.target_id)
     AND far.project_id = $1
     AND far.invalid = false
     AND far.entity_type_id = ANY($8::uuid[])
    WHERE true
),
node_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT e.id, e.key, e.name, et.key, s0.set_name, $11::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $12::integer
    FROM s0
    JOIN entities e ON e.id = s0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
  UNION
    SELECT e.id, e.key, e.name, et.key, t0.set_name, $13::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $14::integer
    FROM t0
    JOIN entities e ON e.id = t0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
  UNION
    SELECT e.id, e.key, e.name, et.key, t1.set_name, $15::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $16::integer
    FROM t1
    JOIN entities e ON e.id = t1.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank)
        ORDER BY all_rows.id, all_rows.rank
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $17
),
edge_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $18::integer
    FROM t0
    JOIN relations r ON r.id = t0.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
  UNION
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $19::integer
    FROM t1
    JOIN relations r ON r.id = t1.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
  UNION
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $20::integer
    FROM relations r
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
    WHERE r.project_id = $1
      AND r.relation_type_id = ANY($21::uuid[])
      AND (r.source_id IN (SELECT id FROM t0) AND r.target_id IN (SELECT id FROM t0))
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank)
        ORDER BY all_rows.id, all_rows.rank
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $22
)
SELECT 'node' AS kind, n.* FROM node_rows n
UNION ALL
SELECT 'edge' AS kind, e.* FROM edge_rows e
ORDER BY 1, 11, 2
