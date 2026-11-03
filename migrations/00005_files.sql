-- +goose Up

-- relative_path usa siempre «/», sea cual sea el sistema. file_hash es la
-- huella parcial de §7.3 y se calcula al indexar: cuando un archivo falta ya
-- no se puede leer, así que debe estar guardada antes. duration_ms y bitrate
-- son del archivo, porque cada versión de un elemento puede diferir.
CREATE TABLE media_files (
    id TEXT PRIMARY KEY,
    library_path_id TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    file_size INTEGER,
    modified_at TEXT,
    file_hash TEXT,
    container TEXT,
    duration_ms INTEGER,
    bitrate INTEGER,
    probe_status TEXT NOT NULL DEFAULT 'pending',
    missing_since TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (library_path_id) REFERENCES library_paths(id) ON DELETE CASCADE,
    UNIQUE (library_path_id, relative_path)
);

CREATE INDEX idx_media_files_missing
    ON media_files(missing_since)
    WHERE missing_since IS NOT NULL;

CREATE TABLE media_file_items (
    media_file_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    part_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (media_file_id, media_item_id),
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE INDEX idx_file_items_item ON media_file_items(media_item_id);

CREATE TABLE media_streams (
    id TEXT PRIMARY KEY,
    media_file_id TEXT NOT NULL,
    stream_index INTEGER NOT NULL,
    stream_type TEXT NOT NULL,
    codec TEXT,
    profile TEXT,
    level TEXT,
    language TEXT,
    channels INTEGER,
    width INTEGER,
    height INTEGER,
    frame_rate TEXT,
    bitrate INTEGER,
    bit_depth INTEGER,
    color_transfer TEXT,
    color_primaries TEXT,
    hdr_format TEXT,
    is_default INTEGER NOT NULL DEFAULT 0,
    is_forced INTEGER NOT NULL DEFAULT 0,
    stream_metadata_json TEXT,
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE,
    UNIQUE (media_file_id, stream_index)
);

CREATE INDEX idx_media_streams_file ON media_streams(media_file_id);

CREATE TABLE subtitles (
    id TEXT PRIMARY KEY,
    media_file_id TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    language TEXT NOT NULL DEFAULT 'und',
    format TEXT,
    is_default INTEGER NOT NULL DEFAULT 0,
    is_forced INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE,
    UNIQUE (media_file_id, relative_path)
);

-- +goose Down
DROP TABLE subtitles;
DROP TABLE media_streams;
DROP TABLE media_file_items;
DROP TABLE media_files;
