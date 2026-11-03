-- +goose Up

-- Índice de búsqueda de §6.2. Los disparadores lo mantienen en la misma
-- transacción que media_items (§6.3), también en los borrados en cascada. Las
-- temporadas no se indexan: su título es «Temporada N».
CREATE VIRTUAL TABLE media_search USING fts5(
    media_item_id UNINDEXED,
    title,
    original_title,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- media_search no admite índices sobre media_item_id, y el rowid de
-- media_items puede cambiar con VACUUM. Esta tabla localiza la fila de cada
-- elemento sin recorrer todo el índice al borrar.
CREATE TABLE media_search_rows (
    media_item_id TEXT PRIMARY KEY,
    search_rowid INTEGER NOT NULL
);

INSERT INTO media_search (media_item_id, title, original_title)
SELECT id, title, original_title FROM media_items WHERE type <> 'season';

INSERT INTO media_search_rows (media_item_id, search_rowid)
SELECT media_item_id, rowid FROM media_search;

-- +goose StatementBegin
CREATE TRIGGER media_items_search_insert AFTER INSERT ON media_items
WHEN NEW.type <> 'season'
BEGIN
    INSERT INTO media_search (media_item_id, title, original_title)
    VALUES (NEW.id, NEW.title, NEW.original_title);
    INSERT INTO media_search_rows (media_item_id, search_rowid)
    VALUES (NEW.id, last_insert_rowid());
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER media_items_search_update AFTER UPDATE OF title, original_title ON media_items
WHEN NEW.type <> 'season'
BEGIN
    UPDATE media_search SET title = NEW.title, original_title = NEW.original_title
    WHERE rowid = (SELECT search_rowid FROM media_search_rows WHERE media_item_id = NEW.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER media_items_search_delete AFTER DELETE ON media_items
WHEN OLD.type <> 'season'
BEGIN
    DELETE FROM media_search
    WHERE rowid = (SELECT search_rowid FROM media_search_rows WHERE media_item_id = OLD.id);
    DELETE FROM media_search_rows WHERE media_item_id = OLD.id;
END;
-- +goose StatementEnd

-- La cola del Enricher son los elementos principales pendientes.
CREATE INDEX idx_media_items_metadata
    ON media_items(metadata_status)
    WHERE parent_id IS NULL;

-- El título ordenable pasa a generarse con media.SortTitle. El servidor
-- rellena los vacíos al arrancar, antes de atender peticiones.
UPDATE media_items SET sort_title = NULL;

-- +goose Down
DROP INDEX idx_media_items_metadata;
DROP TRIGGER media_items_search_delete;
DROP TRIGGER media_items_search_update;
DROP TRIGGER media_items_search_insert;
DROP TABLE media_search_rows;
DROP TABLE media_search;
