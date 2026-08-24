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

-- name: InsertStorageNodeSamples :execrows
INSERT INTO metric_storage_node_samples (node_name, sampled_at, capacity_bytes, used_bytes, available_bytes, volumes_bytes, databases_bytes, objects_bytes, images_bytes, temporary_bytes)
SELECT s.node_name, sqlc.arg(sampled_at)::timestamptz, s.capacity_bytes, s.used_bytes, s.available_bytes, s.volumes_bytes, s.databases_bytes, s.objects_bytes, s.images_bytes, s.temporary_bytes
FROM (
    SELECT unnest(sqlc.arg(node_names)::text[])        AS node_name,
           unnest(sqlc.arg(capacity_bytes)::bigint[])  AS capacity_bytes,
           unnest(sqlc.arg(used_bytes)::bigint[])      AS used_bytes,
           unnest(sqlc.arg(available_bytes)::bigint[]) AS available_bytes,
           unnest(sqlc.arg(volumes_bytes)::bigint[])   AS volumes_bytes,
           unnest(sqlc.arg(databases_bytes)::bigint[]) AS databases_bytes,
           unnest(sqlc.arg(objects_bytes)::bigint[])   AS objects_bytes,
           unnest(sqlc.arg(images_bytes)::bigint[])    AS images_bytes,
           unnest(sqlc.arg(temporary_bytes)::bigint[]) AS temporary_bytes
) AS s
ON CONFLICT DO NOTHING;

-- used_bytes is nullable (unmeasurable app volumes); the companion
-- used_measured array carries the null flags because unnest has no way to
-- express NULL positions in a bigint array parameter.
-- name: InsertStorageSamples :execrows
INSERT INTO metric_storage_samples (environment_id, service_key, kind, sampled_at, used_bytes, capacity_bytes)
SELECT s.environment_id, s.service_key, s.kind, sqlc.arg(sampled_at)::timestamptz,
       CASE WHEN s.used_measured THEN s.used_bytes END, s.capacity_bytes
FROM (
    SELECT unnest(sqlc.arg(environment_ids)::uuid[]) AS environment_id,
           unnest(sqlc.arg(service_keys)::text[])    AS service_key,
           unnest(sqlc.arg(kinds)::text[])           AS kind,
           unnest(sqlc.arg(used_bytes)::bigint[])    AS used_bytes,
           unnest(sqlc.arg(used_measured)::bool[])   AS used_measured,
           unnest(sqlc.arg(capacity_bytes)::bigint[]) AS capacity_bytes
) AS s
WHERE EXISTS (SELECT 1 FROM environments e WHERE e.id = s.environment_id)
ON CONFLICT DO NOTHING;

-- Current values, not series: the storage views show what is, and history
-- stays in the table for future charts. The since cutoff keeps a dead
-- sampler from serving stale numbers as current.
-- name: CurrentStorageNodeSamples :many
SELECT DISTINCT ON (node_name) node_name, sampled_at, capacity_bytes, used_bytes,
       available_bytes, volumes_bytes, databases_bytes, objects_bytes, images_bytes,
       temporary_bytes
FROM metric_storage_node_samples
WHERE sampled_at >= sqlc.arg(since)::timestamptz
ORDER BY node_name, sampled_at DESC;

-- name: CurrentProjectStorage :many
SELECT DISTINCT ON (s.environment_id, s.service_key, s.kind)
       s.environment_id, s.service_key, s.kind, s.sampled_at, s.used_bytes, s.capacity_bytes
FROM metric_storage_samples s
JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = $1 AND s.sampled_at >= sqlc.arg(since)::timestamptz
ORDER BY s.environment_id, s.service_key, s.kind, s.sampled_at DESC;

-- Retention: plain age cutoff, run hourly by the sampler.
-- name: DeleteAgedAppMetricSamples :execrows
DELETE FROM metric_app_samples WHERE sampled_at < $1;

-- name: DeleteAgedNodeMetricSamples :execrows
DELETE FROM metric_node_samples WHERE sampled_at < $1;

-- name: DeleteAgedEdgeMetricSamples :execrows
DELETE FROM metric_edge_samples WHERE sampled_at < $1;

-- name: DeleteAgedStorageNodeSamples :execrows
DELETE FROM metric_storage_node_samples WHERE sampled_at < $1;

-- name: DeleteAgedStorageSamples :execrows
DELETE FROM metric_storage_samples WHERE sampled_at < $1;
