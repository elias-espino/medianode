-- +goose Up
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_login_at TEXT,
    max_age_rating INTEGER
);

-- Garantiza un único Owner aunque falle la comprobación de la aplicación.
CREATE UNIQUE INDEX idx_users_one_owner ON users(role) WHERE role = 'owner';

CREATE TABLE user_preferences (
    user_id TEXT PRIMARY KEY,
    audio_language TEXT,
    subtitle_language TEXT,
    subtitle_mode TEXT NOT NULL DEFAULT 'default',
    max_bitrate_remote INTEGER,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE devices (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    device_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    client_version TEXT,
    device_identifier TEXT,
    capabilities_json TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at TEXT,
    revoked_at TEXT,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_devices_user ON devices(user_id);

-- Un mismo cliente reutiliza su fila. Una revocada no se reactiva.
CREATE UNIQUE INDEX idx_devices_active_identifier
    ON devices(user_id, device_identifier)
    WHERE device_identifier IS NOT NULL AND revoked_at IS NULL;

CREATE TABLE refresh_tokens (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT NOT NULL,
    rotated_at TEXT,
    revoked_at TEXT,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE
);

CREATE INDEX idx_refresh_tokens_device ON refresh_tokens(device_id);

-- El dispositivo que pide el código aguarda con poll_hash a que un usuario
-- autenticado lo reclame. Sus datos esperan aquí hasta crear la fila de devices.
CREATE TABLE pairing_codes (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    poll_hash TEXT NOT NULL UNIQUE,
    device_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    client_version TEXT,
    device_identifier TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT NOT NULL,
    claimed_by_user_id TEXT,
    claimed_at TEXT,
    consumed_at TEXT,
    FOREIGN KEY (claimed_by_user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE pairing_codes;
DROP TABLE refresh_tokens;
DROP TABLE devices;
DROP TABLE user_preferences;
DROP TABLE users;
