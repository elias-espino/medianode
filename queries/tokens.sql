-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, device_id, token_hash, expires_at)
VALUES (?1, ?2, ?3, ?4);

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash = ?1;

-- name: MarkRefreshTokenRotated :exec
UPDATE refresh_tokens SET rotated_at = ?2 WHERE id = ?1;

-- name: RevokeDeviceTokens :exec
UPDATE refresh_tokens SET revoked_at = ?2
WHERE device_id = ?1 AND revoked_at IS NULL;

-- name: RevokeUnrotatedDeviceTokens :exec
UPDATE refresh_tokens SET revoked_at = ?2
WHERE device_id = ?1 AND revoked_at IS NULL AND rotated_at IS NULL;

-- name: DeleteExpiredRefreshTokens :exec
DELETE FROM refresh_tokens WHERE expires_at < ?1;

-- name: DeleteExpiredDeviceTokens :exec
DELETE FROM refresh_tokens WHERE device_id = ?1 AND expires_at < ?2;

-- name: CreatePairingCode :exec
INSERT INTO pairing_codes (
    id, code_hash, poll_hash, device_name, platform, client_version, device_identifier, expires_at
) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8);

-- name: GetPairingCodeByCode :one
SELECT * FROM pairing_codes WHERE code_hash = ?1;

-- name: GetPairingCodeByPoll :one
SELECT * FROM pairing_codes WHERE poll_hash = ?1;

-- name: ClaimPairingCode :exec
UPDATE pairing_codes SET claimed_by_user_id = ?2, claimed_at = ?3 WHERE id = ?1;

-- name: ConsumePairingCode :exec
UPDATE pairing_codes SET consumed_at = ?2 WHERE id = ?1;

-- name: DeleteExpiredPairingCodes :exec
DELETE FROM pairing_codes WHERE expires_at < ?1;
