-- name: CreateDevice :exec
INSERT INTO devices (id, user_id, device_name, platform, client_version, device_identifier, last_seen_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7);

-- name: GetDevice :one
SELECT * FROM devices WHERE id = ?1;

-- name: GetActiveDeviceByIdentifier :one
SELECT * FROM devices
WHERE user_id = ?1 AND device_identifier = ?2 AND revoked_at IS NULL;

-- name: ListDevices :many
SELECT * FROM devices ORDER BY id;

-- name: ListUserDevices :many
SELECT * FROM devices WHERE user_id = ?1 ORDER BY id;

-- name: UpdateDeviceOnLogin :exec
UPDATE devices SET
    device_name = ?2,
    platform = ?3,
    client_version = ?4,
    last_seen_at = ?5
WHERE id = ?1;

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = ?2 WHERE id = ?1;

-- name: RenameDevice :exec
UPDATE devices SET device_name = ?2 WHERE id = ?1;

-- name: RevokeDevice :exec
UPDATE devices SET revoked_at = ?2 WHERE id = ?1 AND revoked_at IS NULL;

-- name: ListActiveUserDeviceIDs :many
SELECT id FROM devices WHERE user_id = ?1 AND revoked_at IS NULL;

-- name: GetAuthState :one
SELECT d.revoked_at, u.role, u.is_active, u.max_age_rating
FROM devices d
JOIN users u ON u.id = d.user_id
WHERE d.id = ?1 AND d.user_id = ?2;
