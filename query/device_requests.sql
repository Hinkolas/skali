-- name: CreateDeviceRequest :one
INSERT INTO device_requests (id, intent, user_code, device_code_hash, session_id, user_id, client_label, ip_address, user_agent, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetDeviceRequestByUserCode :one
SELECT * FROM device_requests WHERE user_code = $1 AND expires_at > now();

-- name: GetDeviceRequestByDeviceCodeHashForUpdate :one
SELECT * FROM device_requests WHERE device_code_hash = $1 FOR UPDATE;

-- name: TouchDeviceRequestPoll :one
UPDATE device_requests SET poll_count = poll_count + 1, last_polled_at = $2
WHERE id = $1
RETURNING poll_count, last_polled_at;

-- name: ApproveDeviceRequest :execrows
UPDATE device_requests SET status = 'approved', user_id = $2
WHERE id = $1 AND status = 'pending' AND expires_at > now();

-- name: DenyDeviceRequest :execrows
UPDATE device_requests SET status = 'denied'
WHERE id = $1 AND status = 'pending' AND expires_at > now();

-- name: DeleteDeviceRequest :exec
DELETE FROM device_requests WHERE id = $1;

-- name: DeleteExpiredDeviceRequests :execrows
DELETE FROM device_requests WHERE expires_at <= now();
