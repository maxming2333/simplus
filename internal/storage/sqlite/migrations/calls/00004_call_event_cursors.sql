-- +goose Up
-- Inbound call notifications are read from a bridge's in-memory ring, which the
-- bridge never deletes from. A consumer's read position therefore has to be
-- durable here rather than on the device, unlike stored messages where
-- acknowledgement removes the message and no cursor is needed.
--
-- The read position is only meaningful together with the two facts that scope it.
-- boot_id changes when the bridge restarts, and its sequences restart at zero, so
-- a cursor carried across a restart would otherwise never match another event
-- again. subscription_fingerprint changes when the SIM changes, and the ring
-- outlives that, so entries still held arrived under the previous subscription and
-- must be skipped rather than attributed to the current one.
CREATE TABLE call_event_cursors (
    device_id TEXT PRIMARY KEY CHECK (length(device_id) BETWEEN 1 AND 128),
    boot_id TEXT NOT NULL CHECK (length(boot_id) = 16),
    subscription_fingerprint TEXT NOT NULL CHECK (length(subscription_fingerprint) = 64),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms > 0)
) WITHOUT ROWID;

UPDATE dataset_metadata SET schema_version = 4 WHERE singleton = 1;

-- +goose Down
DROP TABLE call_event_cursors;
UPDATE dataset_metadata SET schema_version = 3 WHERE singleton = 1;
