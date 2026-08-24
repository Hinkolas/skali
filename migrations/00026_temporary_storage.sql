-- +goose Up

-- Temporary (ephemeral) storage joins the node category split: the scratch
-- space containers consume on the node filesystem (writable layers, logs,
-- emptyDirs), read from the kubelet's per-pod stats. Backfilled rows read
-- zero; the remainder math at read time treats them like any other
-- category.
ALTER TABLE metric_storage_node_samples
    ADD COLUMN temporary_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE metric_storage_node_samples DROP COLUMN temporary_bytes;
