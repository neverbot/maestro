WITH RECURSIVE s0 (id, set_name) AS (
    SELECT e.id, $4::text
    FROM entities e
    WHERE e.project_id = $1
      AND e.entity_type_id = $2
      AND e.invalid = false
      AND (lower(e.key) = $3)
),
t0 (id, set_name, from_id, via_relation) AS (
    SELECT r.source_id, $10::text, near.id, r.id
    FROM s0 near
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($11::uuid[])
     AND r.target_id = near.id
     AND true
    JOIN entities far
      ON far.id = (r.source_id)
     AND far.project_id = $1
     AND far.invalid = false
     AND far.entity_type_id = ANY($9::uuid[])
    WHERE ((jsonb_typeof((far.fields -> $5)) = 'number' AND (far.fields ->> $5)::numeric >= $6) AND (jsonb_typeof((far.fields -> $7)) = 'number' AND (far.fields ->> $7)::numeric <= $8))
),
node_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank) AS (
    SELECT e.id, e.key, e.name, et.key, s0.set_name, $12::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $13::integer
    FROM s0
    JOIN entities e ON e.id = s0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
  UNION
    SELECT e.id, e.key, e.name, et.key, t0.set_name, $14::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $15::integer
    FROM t0
    JOIN entities e ON e.id = t0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
    ORDER BY 10, 1
    LIMIT $16
),
edge_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank) AS (
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $17::integer
    FROM t0
    JOIN relations r ON r.id = t0.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
  UNION
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $18::integer
    FROM relations r
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
    WHERE r.project_id = $1
      AND r.relation_type_id = ANY($19::uuid[])
      AND (r.source_id IN (SELECT id FROM t0) AND r.target_id IN (SELECT id FROM t0))
    ORDER BY 10, 1
    LIMIT $20
)
SELECT 'node' AS kind, n.* FROM node_rows n
UNION ALL
SELECT 'edge' AS kind, e.* FROM edge_rows e
ORDER BY 1, 11, 2
