-- +goose Up

-- updated_at es la hora del dato, no la de escritura: la del heartbeat o la
-- que envía el cliente si no es futura. Así un dato atrasado de otro
-- dispositivo no pisa uno reciente (§12.3).
CREATE TABLE playback_progress (
    user_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    position_ms INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    completed INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, media_item_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE INDEX idx_progress_updated ON playback_progress(updated_at);

-- position_ms y duration_ms guardan el último estado volcado, para cerrar la
-- sesión con su posición tras una caída. start_position_ms es donde empezó.
CREATE TABLE playback_sessions (
    id TEXT PRIMARY KEY,
    client_kind TEXT NOT NULL DEFAULT 'app',
    user_id TEXT,
    device_id TEXT,
    media_item_id TEXT NOT NULL,
    media_file_id TEXT NOT NULL,
    selected_audio_stream_id TEXT,
    selected_subtitle_id TEXT,
    playback_mode TEXT NOT NULL,
    status TEXT NOT NULL,
    start_position_ms INTEGER NOT NULL DEFAULT 0,
    position_ms INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_heartbeat_at TEXT,
    last_activity_at TEXT,
    ended_at TEXT,
    client_ip TEXT,
    CHECK (client_kind = 'dlna'
        OR (user_id IS NOT NULL AND device_id IS NOT NULL)),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE
);

CREATE INDEX idx_sessions_user_status ON playback_sessions(user_id, status);
CREATE INDEX idx_sessions_activity ON playback_sessions(last_activity_at);
CREATE INDEX idx_sessions_open ON playback_sessions(status) WHERE ended_at IS NULL;

CREATE TABLE playback_history (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    device_id TEXT,
    media_item_id TEXT,
    title TEXT NOT NULL,
    position_ms INTEGER NOT NULL DEFAULT 0,
    completed INTEGER NOT NULL DEFAULT 0,
    watched_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE SET NULL,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE SET NULL
);

CREATE INDEX idx_history_user ON playback_history(user_id, watched_at);

CREATE TABLE transcode_jobs (
    id TEXT PRIMARY KEY,
    playback_session_id TEXT NOT NULL,
    job_kind TEXT NOT NULL,
    status TEXT NOT NULL,
    input_file_id TEXT NOT NULL,
    output_profile TEXT NOT NULL,
    start_position_ms INTEGER NOT NULL DEFAULT 0,
    progress REAL NOT NULL DEFAULT 0,
    started_at TEXT,
    finished_at TEXT,
    error_code TEXT,
    error_message TEXT,
    FOREIGN KEY (playback_session_id) REFERENCES playback_sessions(id)
        ON DELETE CASCADE,
    FOREIGN KEY (input_file_id) REFERENCES media_files(id) ON DELETE CASCADE
);

CREATE INDEX idx_transcode_status ON transcode_jobs(status);

CREATE UNIQUE INDEX idx_transcode_one_active
    ON transcode_jobs(playback_session_id)
    WHERE status IN ('starting', 'running');

-- +goose Down
DROP TABLE transcode_jobs;
DROP TABLE playback_history;
DROP TABLE playback_sessions;
DROP TABLE playback_progress;
