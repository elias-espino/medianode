-- name: CreateUser :exec
INSERT INTO users (id, username, password_hash, role, max_age_rating)
VALUES (?1, ?2, ?3, ?4, ?5);

-- name: GetUser :one
SELECT * FROM users WHERE id = ?1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ?1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY username;

-- name: CountOwners :one
SELECT COUNT(*) FROM users WHERE role = 'owner';

-- name: UpdateUser :exec
UPDATE users SET
    username = ?2,
    role = ?3,
    is_active = ?4,
    max_age_rating = ?5,
    updated_at = ?6
WHERE id = ?1;

-- name: UpdatePassword :exec
UPDATE users SET password_hash = ?2, updated_at = ?3 WHERE id = ?1;

-- name: TouchLastLogin :exec
UPDATE users SET last_login_at = ?2 WHERE id = ?1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?1;

-- name: CreatePreferences :exec
INSERT INTO user_preferences (user_id) VALUES (?1);

-- name: GetPreferences :one
SELECT * FROM user_preferences WHERE user_id = ?1;

-- name: SetPreferences :exec
UPDATE user_preferences SET
    audio_language = ?2,
    subtitle_language = ?3,
    subtitle_mode = ?4,
    max_bitrate_remote = ?5
WHERE user_id = ?1;
