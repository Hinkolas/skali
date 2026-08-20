-- Usage telemetry written by the metrics sampler and read bucketed by the
-- metrics API. Inserts are batched via parallel unnest arrays: one statement
-- per tick regardless of app count.

-- The EXISTS guard tolerates the race where a pod still runs (labels intact)
-- after its environment row was deleted: the batch skips that row instead of
-- failing on the foreign key.
-- name: InsertAppMetricSamples :execrows
INSERT INTO metric_app_samples (environment_id, application_key, sampled_at, cpu_millicores, memory_bytes, pod_count)
SELECT s.environment_id, s.application_key, sqlc.arg(sampled_at)::timestamptz, s.cpu_millicores, s.memory_bytes, s.pod_count
FROM (
    SELECT unnest(sqlc.arg(environment_ids)::uuid[])  AS environment_id,
           unnest(sqlc.arg(application_keys)::text[]) AS application_key,
           unnest(sqlc.arg(cpu_millicores)::bigint[]) AS cpu_millicores,
           unnest(sqlc.arg(memory_bytes)::bigint[])   AS memory_bytes,
           unnest(sqlc.arg(pod_counts)::bigint[])     AS pod_count
) AS s
WHERE EXISTS (SELECT 1 FROM environments e WHERE e.id = s.environment_id)
ON CONFLICT DO NOTHING;

-- name: InsertNodeMetricSamples :execrows
INSERT INTO metric_node_samples (node_name, sampled_at, cpu_millicores, memory_bytes, cpu_allocatable_millicores, memory_allocatable_bytes)
SELECT s.node_name, sqlc.arg(sampled_at)::timestamptz, s.cpu_millicores, s.memory_bytes, s.cpu_allocatable_millicores, s.memory_allocatable_bytes
FROM (
    SELECT unnest(sqlc.arg(node_names)::text[])                     AS node_name,
           unnest(sqlc.arg(cpu_millicores)::bigint[])               AS cpu_millicores,
           unnest(sqlc.arg(memory_bytes)::bigint[])                 AS memory_bytes,
           unnest(sqlc.arg(cpu_allocatable_millicores)::bigint[])   AS cpu_allocatable_millicores,
           unnest(sqlc.arg(memory_allocatable_bytes)::bigint[])     AS memory_allocatable_bytes
) AS s
ON CONFLICT DO NOTHING;

-- Bucketed series for the chart API: date_bin aligns samples onto the fixed
-- step grid anchored at origin, and absent buckets simply produce no row; the
-- handler rebuilds the full grid and leaves nulls for the gaps.
-- name: AppMetricSeries :many
SELECT (date_bin(make_interval(secs => sqlc.arg(step_seconds)::int), sampled_at, sqlc.arg(origin)::timestamptz))::timestamptz AS bucket,
       application_key,
       (avg(cpu_millicores))::bigint AS cpu_millicores,
       (avg(memory_bytes))::bigint   AS memory_bytes
FROM metric_app_samples
WHERE environment_id = $1
  AND sampled_at >= sqlc.arg(since)::timestamptz
  AND sampled_at < sqlc.arg(until)::timestamptz
GROUP BY bucket, application_key
ORDER BY application_key, bucket;

-- name: NodeMetricSeries :many
SELECT (date_bin(make_interval(secs => sqlc.arg(step_seconds)::int), sampled_at, sqlc.arg(origin)::timestamptz))::timestamptz AS bucket,
       node_name,
       (avg(cpu_millicores))::bigint       AS cpu_millicores,
       (avg(memory_bytes))::bigint         AS memory_bytes,
       (max(cpu_allocatable_millicores))::bigint AS cpu_allocatable_millicores,
       (max(memory_allocatable_bytes))::bigint   AS memory_allocatable_bytes
FROM metric_node_samples
WHERE sampled_at >= sqlc.arg(since)::timestamptz
  AND sampled_at < sqlc.arg(until)::timestamptz
GROUP BY bucket, node_name
ORDER BY node_name, bucket;

-- name: InsertEdgeMetricSamples :execrows
INSERT INTO metric_edge_samples (environment_id, application_key, route_key, sampled_at, requests, request_bytes, response_bytes)
SELECT s.environment_id, s.application_key, s.route_key, sqlc.arg(sampled_at)::timestamptz, s.requests, s.request_bytes, s.response_bytes
FROM (
    SELECT unnest(sqlc.arg(environment_ids)::uuid[])  AS environment_id,
           unnest(sqlc.arg(application_keys)::text[]) AS application_key,
           unnest(sqlc.arg(route_keys)::text[])       AS route_key,
           unnest(sqlc.arg(requests)::bigint[])       AS requests,
           unnest(sqlc.arg(request_bytes)::bigint[])  AS request_bytes,
           unnest(sqlc.arg(response_bytes)::bigint[]) AS response_bytes
) AS s
WHERE EXISTS (SELECT 1 FROM environments e WHERE e.id = s.environment_id)
ON CONFLICT DO NOTHING;

-- Edge deltas sum per bucket (they are per-interval counts, not gauges), so
-- a bucket's value is the traffic that arrived within it.
-- name: EdgeMetricSeries :many
SELECT (date_bin(make_interval(secs => sqlc.arg(step_seconds)::int), sampled_at, sqlc.arg(origin)::timestamptz))::timestamptz AS bucket,
       application_key,
       (sum(requests))::bigint       AS requests,
       (sum(request_bytes))::bigint  AS request_bytes,
       (sum(response_bytes))::bigint AS response_bytes
FROM metric_edge_samples
WHERE environment_id = $1
  AND sampled_at >= sqlc.arg(since)::timestamptz
  AND sampled_at < sqlc.arg(until)::timestamptz
GROUP BY bucket, application_key
ORDER BY application_key, bucket;

-- Retention: plain age cutoff, run hourly by the sampler.
-- name: DeleteAgedAppMetricSamples :execrows
DELETE FROM metric_app_samples WHERE sampled_at < $1;

-- name: DeleteAgedNodeMetricSamples :execrows
DELETE FROM metric_node_samples WHERE sampled_at < $1;

-- name: DeleteAgedEdgeMetricSamples :execrows
DELETE FROM metric_edge_samples WHERE sampled_at < $1;
