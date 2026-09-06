-- case: out_basic
w (id, depth, path, via_relation, from_id, closed) AS (
    SELECT seed.id, 0, ARRAY[seed.id], NULL::uuid, NULL::uuid, false
    FROM (SELECT id FROM entities WHERE project_id = $2 AND id = $3) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT r.target_id, w.depth + 1, w.path || (r.target_id), r.id, w.id, (r.target_id) = ANY(w.path)
    FROM w w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($4::uuid[])
     AND r.id IS DISTINCT FROM w.via_relation
     AND r.source_id = w.id
    JOIN entities far
      ON far.id = (r.target_id)
     AND far.project_id = $1
    WHERE w.depth < $5
      AND NOT w.closed
),
w_out AS (SELECT * FROM w WHERE depth >= $6 ORDER BY depth)

-- case: any_predicate_capped
s3 (id, depth, path, via_relation, from_id, closed) AS (
    SELECT seed.id, 0, ARRAY[seed.id], NULL::uuid, NULL::uuid, false
    FROM (SELECT id FROM entities WHERE project_id = $2 AND entity_type_id = $3) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END, w.depth + 1, w.path || (CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END), r.id, w.id, (CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END) = ANY(w.path)
    FROM s3 w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($6::uuid[])
     AND r.id IS DISTINCT FROM w.via_relation
     AND (r.source_id = w.id OR r.target_id = w.id)
     AND (((r.relation_type_id = ANY($4::uuid[]) AND r.source_id = w.id) OR (r.relation_type_id = ANY($5::uuid[]) AND r.target_id = w.id)))
    JOIN entities far
      ON far.id = (CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END)
     AND far.project_id = $1
    WHERE w.depth < $7
      AND NOT w.closed
),
s3_out AS (SELECT * FROM s3 WHERE depth >= $8 ORDER BY depth LIMIT $9)

-- case: in_min_depth_capped
t1 (id, depth, path, via_relation, from_id, closed) AS (
    SELECT seed.id, 0, ARRAY[seed.id], NULL::uuid, NULL::uuid, false
    FROM (SELECT id FROM entities WHERE project_id = $2) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT r.source_id, w.depth + 1, w.path || (r.source_id), r.id, w.id, (r.source_id) = ANY(w.path)
    FROM t1 w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($3::uuid[])
     AND r.id IS DISTINCT FROM w.via_relation
     AND r.target_id = w.id
    JOIN entities far
      ON far.id = (r.source_id)
     AND far.project_id = $1
    WHERE w.depth < $4
      AND NOT w.closed
),
t1_out AS (SELECT * FROM t1 WHERE depth >= $5 ORDER BY depth LIMIT $6)

