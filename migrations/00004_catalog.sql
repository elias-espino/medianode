-- +goose Up

-- match_key es la clave de agrupación local (título normalizado y año, sacados
-- del nombre del archivo). Los metadatos pueden cambiar title; match_key no
-- cambia, para que un archivo nuevo encuentre el mismo elemento.
CREATE TABLE media_items (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL,
    parent_id TEXT,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    original_title TEXT,
    sort_title TEXT,
    year INTEGER,
    release_date TEXT,
    overview TEXT,
    age_rating TEXT,
    min_age INTEGER,
    original_language TEXT,
    duration_ms INTEGER,
    season_number INTEGER,
    episode_number INTEGER,
    match_key TEXT,
    metadata_status TEXT NOT NULL DEFAULT 'pending',
    metadata_locked INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE INDEX idx_media_items_library ON media_items(library_id);
CREATE INDEX idx_media_items_parent ON media_items(parent_id);
CREATE INDEX idx_media_items_sort ON media_items(library_id, sort_title);

CREATE UNIQUE INDEX idx_media_items_match
    ON media_items(library_id, type, match_key)
    WHERE parent_id IS NULL AND match_key IS NOT NULL;

CREATE UNIQUE INDEX idx_media_items_season
    ON media_items(parent_id, season_number)
    WHERE type = 'season';

CREATE UNIQUE INDEX idx_media_items_episode
    ON media_items(parent_id, episode_number)
    WHERE type = 'episode';

CREATE TABLE media_external_ids (
    media_item_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    PRIMARY KEY (media_item_id, provider),
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE INDEX idx_external_ids_lookup ON media_external_ids(provider, external_id);

CREATE TABLE genres (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE media_item_genres (
    media_item_id TEXT NOT NULL,
    genre_id TEXT NOT NULL,
    PRIMARY KEY (media_item_id, genre_id),
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
);

CREATE TABLE people (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE media_item_people (
    media_item_id TEXT NOT NULL,
    person_id TEXT NOT NULL,
    role TEXT NOT NULL,
    character_name TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (media_item_id, person_id, role),
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    FOREIGN KEY (person_id) REFERENCES people(id) ON DELETE CASCADE
);

CREATE TABLE media_images (
    id TEXT PRIMARY KEY,
    media_item_id TEXT NOT NULL,
    type TEXT NOT NULL,
    local_path TEXT,
    source_url TEXT,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    UNIQUE (media_item_id, type)
);

-- +goose Down
DROP TABLE media_images;
DROP TABLE media_item_people;
DROP TABLE people;
DROP TABLE media_item_genres;
DROP TABLE genres;
DROP TABLE media_external_ids;
DROP TABLE media_items;
