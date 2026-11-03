-- name: GetSetting :one
SELECT value FROM server_settings WHERE key = ?1;

-- name: ListSettings :many
SELECT key, value, updated_at FROM server_settings ORDER BY key;

-- name: SetSetting :exec
INSERT INTO server_settings (key, value)
VALUES (?1, ?2)
ON CONFLICT (key) DO UPDATE SET
    value = excluded.value,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DeleteSetting :exec
DELETE FROM server_settings WHERE key = ?1;
