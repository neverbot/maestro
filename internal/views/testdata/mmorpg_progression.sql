WITH RECURSIVE s0 (id, set_name, depth) AS (
    SELECT e.id, $4::text, 0
    FROM entities e
    WHERE e.project_id = $1
      AND e.entity_type_id = $2
      AND e.invalid = false
      AND (lower(e.key) = $3)
),
t0 (id, set_name, from_id, via_relation, depth) AS (
    SELECT r.source_id, $6::text, near.id, r.id, near.depth + 1
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
w1 (id, depth, path, via_relation, from_id, closed) AS (
    SELECT seed.id, 0, ARRAY[seed.id], NULL::uuid, NULL::uuid, false
    FROM (SELECT DISTINCT id FROM t0) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT r.target_id, w.depth + 1, w.path || (r.target_id), r.id, w.id, (r.target_id) = ANY(w.path)
    FROM w1 w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($9::uuid[])
     AND r.id IS DISTINCT FROM w.via_relation
     AND r.source_id = w.id
     AND ((r.relation_type_id = $8))
    JOIN entities far
      ON far.id = (r.target_id)
     AND far.project_id = $1
    WHERE w.depth < $10
      AND NOT w.closed
),
w1_out AS (SELECT * FROM w1 WHERE depth >= $11 ORDER BY depth LIMIT $12),
t1 (id, set_name, from_id, via_relation, depth) AS (
    SELECT w.id, $16::text, w.from_id, w.via_relation, src.depth + w.depth
    FROM (SELECT * FROM w1_out LIMIT $14) w
    JOIN (SELECT id, MIN(depth) AS depth FROM t0 GROUP BY id) src ON src.id = w.path[1]
    JOIN entities far
      ON far.id = w.id
     AND far.project_id = $1
     AND far.invalid = false
     AND far.entity_type_id = ANY($15::uuid[])
    WHERE w.depth <= $13
      AND true
),
t2 (id, set_name, from_id, via_relation, depth) AS (
    SELECT r.target_id, $18::text, near.id, r.id, near.depth + 1
    FROM t0 near
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($19::uuid[])
     AND r.source_id = near.id
     AND true
    JOIN entities far
      ON far.id = (r.target_id)
     AND far.project_id = $1
     AND far.invalid = false
     AND far.entity_type_id = ANY($17::uuid[])
    WHERE true
),
node_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT e.id, e.key, e.name, et.key, s0.set_name, $20::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $21::integer, s0.depth
    FROM s0
    JOIN entities e ON e.id = s0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
  UNION
    SELECT e.id, e.key, e.name, et.key, t0.set_name, $22::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $23::integer, t0.depth
    FROM t0
    JOIN entities e ON e.id = t0.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
  UNION
    SELECT e.id, e.key, e.name, et.key, t1.set_name, $24::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $25::integer, t1.depth
    FROM t1
    JOIN entities e ON e.id = t1.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
  UNION
    SELECT e.id, e.key, e.name, et.key, t2.set_name, $26::text,
           NULL::uuid, NULL::uuid, NULL::jsonb, $27::integer, t2.depth
    FROM t2
    JOIN entities e ON e.id = t2.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth)
        ORDER BY all_rows.id, all_rows.rank, all_rows.depth
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $28
),
edge_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth) AS (
    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $29::integer, t0.depth
    FROM t0
    JOIN relations r ON r.id = t0.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
  UNION
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $30::integer, t1.depth
    FROM t1
    JOIN relations r ON r.id = t1.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
  UNION
    SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, NULL::jsonb, $31::integer, t2.depth
    FROM t2
    JOIN relations r ON r.id = t2.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
        ) AS all_rows (id, key, name, type_key, set_name, role, source_id, target_id, fields, rank, depth)
        ORDER BY all_rows.id, all_rows.rank, all_rows.depth
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT $32
)
SELECT 'node' AS kind, n.* FROM node_rows n
UNION ALL
SELECT 'edge' AS kind, e.* FROM edge_rows e
UNION ALL
SELECT 'depth_truncated' AS kind, NULL::uuid, NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::uuid, NULL::uuid, NULL::jsonb, NULL::integer, NULL::integer
WHERE EXISTS (
        SELECT 1 FROM w1 deep
        WHERE deep.depth > $13
          AND (deep.id NOT IN (SELECT id FROM w1 WHERE depth <= $13)
            OR deep.via_relation NOT IN (SELECT via_relation FROM w1
                                         WHERE depth <= $13 AND via_relation IS NOT NULL))
    )
UNION ALL
SELECT 'walk_truncated' AS kind, NULL::uuid, NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::uuid, NULL::uuid, NULL::jsonb, NULL::integer, NULL::integer
WHERE EXISTS (SELECT 1 FROM w1_out OFFSET $14)
ORDER BY 1, 11, 2
