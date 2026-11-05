-- DLNA browsing (section 13.2). A renderer has no user: what it can see is
-- decided only by libraries.enabled and libraries.dlna_enabled. Comments must
-- stay ASCII: sqlc miscalculates offsets otherwise (see database/queries_test.go).

-- name: ListDLNALibraries :many
SELECT id, name, type FROM libraries
WHERE enabled = 1 AND dlna_enabled = 1
ORDER BY name, id;

-- name: GetDLNALibrary :one
SELECT id, name, type FROM libraries
WHERE id = ?1 AND enabled = 1 AND dlna_enabled = 1;

-- name: GetDLNAItem :one
SELECT mi.id, mi.library_id, mi.parent_id, mi.type, mi.title, mi.year, mi.overview,
       mi.release_date, mi.duration_ms, mi.season_number, mi.episode_number, mi.age_rating,
       lib.type AS library_type
FROM media_items mi
JOIN libraries lib ON lib.id = mi.library_id
WHERE mi.id = ?1 AND lib.enabled = 1 AND lib.dlna_enabled = 1;

-- name: CountDLNALibraryItems :one
SELECT COUNT(*) FROM media_items mi
WHERE mi.library_id = ?1 AND mi.parent_id IS NULL
  AND (mi.type NOT IN ('movie', 'episode') OR EXISTS (
        SELECT 1 FROM media_file_items l
        JOIN media_files f ON f.id = l.media_file_id
        WHERE l.media_item_id = mi.id AND f.missing_since IS NULL AND f.probe_status = 'done'));

-- name: ListDLNALibraryItems :many
SELECT mi.id, mi.library_id, mi.parent_id, mi.type, mi.title, mi.year, mi.overview,
       mi.release_date, mi.duration_ms, mi.season_number, mi.episode_number, mi.age_rating
FROM media_items mi
WHERE mi.library_id = sqlc.arg(library_id) AND mi.parent_id IS NULL
  AND (mi.type NOT IN ('movie', 'episode') OR EXISTS (
        SELECT 1 FROM media_file_items l
        JOIN media_files f ON f.id = l.media_file_id
        WHERE l.media_item_id = mi.id AND f.missing_since IS NULL AND f.probe_status = 'done'))
ORDER BY mi.sort_title, mi.id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: ListDLNALibraryItemsDesc :many
SELECT mi.id, mi.library_id, mi.parent_id, mi.type, mi.title, mi.year, mi.overview,
       mi.release_date, mi.duration_ms, mi.season_number, mi.episode_number, mi.age_rating
FROM media_items mi
WHERE mi.library_id = sqlc.arg(library_id) AND mi.parent_id IS NULL
  AND (mi.type NOT IN ('movie', 'episode') OR EXISTS (
        SELECT 1 FROM media_file_items l
        JOIN media_files f ON f.id = l.media_file_id
        WHERE l.media_item_id = mi.id AND f.missing_since IS NULL AND f.probe_status = 'done'))
ORDER BY mi.sort_title DESC, mi.id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountDLNAChildren :one
SELECT COUNT(*) FROM media_items mi
WHERE mi.parent_id = ?1
  AND (mi.type NOT IN ('movie', 'episode') OR EXISTS (
        SELECT 1 FROM media_file_items l
        JOIN media_files f ON f.id = l.media_file_id
        WHERE l.media_item_id = mi.id AND f.missing_since IS NULL AND f.probe_status = 'done'));

-- name: ListDLNAChildren :many
SELECT mi.id, mi.library_id, mi.parent_id, mi.type, mi.title, mi.year, mi.overview,
       mi.release_date, mi.duration_ms, mi.season_number, mi.episode_number, mi.age_rating
FROM media_items mi
WHERE mi.parent_id = sqlc.arg(parent_id)
  AND (mi.type NOT IN ('movie', 'episode') OR EXISTS (
        SELECT 1 FROM media_file_items l
        JOIN media_files f ON f.id = l.media_file_id
        WHERE l.media_item_id = mi.id AND f.missing_since IS NULL AND f.probe_status = 'done'))
ORDER BY COALESCE(mi.season_number, 0), COALESCE(mi.episode_number, 0), mi.id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: GetDLNAFileItem :one
SELECT mi.id AS item_id, mi.library_id, mi.title
FROM media_file_items l
JOIN media_items mi ON mi.id = l.media_item_id
JOIN libraries lib ON lib.id = mi.library_id
WHERE l.media_file_id = ?1 AND lib.enabled = 1 AND lib.dlna_enabled = 1
ORDER BY l.part_order, mi.id
LIMIT 1;

-- name: GetDLNAUpdateStamp :one
SELECT CAST(COALESCE(MAX(stamp), '') AS TEXT) AS stamp FROM (
  SELECT MAX(updated_at) AS stamp FROM media_items
  UNION ALL
  SELECT MAX(updated_at) AS stamp FROM libraries
);

-- name: ListDLNASearchCandidates :many
SELECT mi.id, mi.library_id, mi.parent_id, mi.type, mi.title, mi.year, mi.overview,
       mi.release_date, mi.duration_ms, mi.season_number, mi.episode_number, mi.age_rating,
       lib.type AS library_type, p.parent_id AS grandparent_id
FROM media_items mi
JOIN libraries lib ON lib.id = mi.library_id
LEFT JOIN media_items p ON p.id = mi.parent_id
WHERE lib.enabled = 1 AND lib.dlna_enabled = 1
  AND mi.type IN ('movie', 'episode')
  AND EXISTS (
        SELECT 1 FROM media_file_items l
        JOIN media_files f ON f.id = l.media_file_id
        WHERE l.media_item_id = mi.id AND f.missing_since IS NULL AND f.probe_status = 'done')
ORDER BY mi.sort_title, mi.id
LIMIT sqlc.arg(max_rows);
