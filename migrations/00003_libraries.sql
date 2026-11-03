-- +goose Up
CREATE TABLE libraries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    dlna_enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Dos bibliotecas pueden compartir una ruta; una misma biblioteca no la repite.
CREATE TABLE library_paths (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL,
    path TEXT NOT NULL,
    is_available INTEGER NOT NULL DEFAULT 1,
    last_scan_at TEXT,
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE,
    UNIQUE (library_id, path)
);

CREATE TABLE user_permissions (
    user_id TEXT NOT NULL,
    library_id TEXT NOT NULL,
    permission TEXT NOT NULL,
    PRIMARY KEY (user_id, library_id, permission),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE
);

CREATE INDEX idx_user_permissions_library ON user_permissions(library_id);

CREATE TABLE scan_jobs (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    files_discovered INTEGER NOT NULL DEFAULT 0,
    files_processed INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE
);

CREATE INDEX idx_scan_library_status ON scan_jobs(library_id, status);

-- Un solo escaneo en curso por biblioteca, aunque falle la comprobación en
-- memoria. Al arrancar, los que quedaron en running pasan a interrupted.
CREATE UNIQUE INDEX idx_scan_one_running ON scan_jobs(library_id) WHERE status = 'running';

-- +goose Down
DROP TABLE scan_jobs;
DROP TABLE user_permissions;
DROP TABLE library_paths;
DROP TABLE libraries;
