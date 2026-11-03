-- Read queries for the catalog API. Comments must stay ASCII: sqlc
-- miscalculates offsets otherwise (see database/queries_test.go).

-- name: ListLibraryItems :many
SELECT mi.id, mi.type, mi.title, mi.year, mi.sort_title, mi.metadata_status,
       CAST(EXISTS (
         SELECT 1 FROM media_images im
         WHERE im.media_item_id = mi.id AND im.type = 'poster' AND im.local_path IS NOT NULL
       ) AS INTEGER) AS has_poster
FROM media_items mi
WHERE mi.library_id = sqlc.arg(library_id)
  AND mi.parent_id IS NULL
  AND (mi.sort_title, mi.id) > (sqlc.arg(after_sort), sqlc.arg(after_id))
  AND (CAST(sqlc.arg(restricted) AS INTEGER) = 0 OR (mi.min_age >= 0 AND mi.min_age <= sqlc.arg(max_age)))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR mi.metadata_status = sqlc.arg(status))
ORDER BY mi.sort_title, mi.id
LIMIT sqlc.arg(page_size);

-- name: ListChildren :many
SELECT c.id, c.type, c.title, c.year, c.season_number, c.episode_number, c.overview,
       c.release_date, c.duration_ms,
       CAST(COALESCE(c.min_age, p.min_age, g.min_age, -1) AS INTEGER) AS effective_min_age,
       CAST(EXISTS (
         SELECT 1 FROM media_images im
         WHERE im.media_item_id = c.id AND im.local_path IS NOT NULL
       ) AS INTEGER) AS has_image
FROM media_items c
JOIN media_items p ON p.id = c.parent_id
LEFT JOIN media_items g ON g.id = p.parent_id
WHERE c.parent_id = ?1
ORDER BY COALESCE(c.season_number, 0), COALESCE(c.episode_number, 0), c.id;

-- name: ListItemGenres :many
SELECT g.name FROM genres g
JOIN media_item_genres mg ON mg.genre_id = g.id
WHERE mg.media_item_id = ?1
ORDER BY g.name;

-- name: ListItemPeople :many
SELECT p.id, p.name, mp.role, mp.character_name, mp.sort_order
FROM media_item_people mp
JOIN people p ON p.id = mp.person_id
WHERE mp.media_item_id = ?1
ORDER BY mp.role, mp.sort_order, p.name;

-- name: ListItemExternalIDs :many
SELECT provider, external_id FROM media_external_ids
WHERE media_item_id = ?1
ORDER BY provider;

-- name: ListItemImages :many
SELECT type, local_path, source_url FROM media_images
WHERE media_item_id = ?1
ORDER BY type;

-- name: GetItemImage :one
SELECT local_path FROM media_images
WHERE media_item_id = ?1 AND type = ?2 AND local_path IS NOT NULL;

-- name: ListItemFiles :many
SELECT f.id, f.relative_path, f.container, f.file_size, f.duration_ms, f.bitrate,
       f.probe_status, f.missing_since, l.part_order
FROM media_file_items l
JOIN media_files f ON f.id = l.media_file_id
WHERE l.media_item_id = ?1
ORDER BY f.missing_since IS NOT NULL, f.relative_path;

-- name: ListItemStreams :many
SELECT s.id, s.media_file_id, s.stream_index, s.stream_type, s.codec, s.profile, s.level,
       s.language, s.channels, s.width, s.height, s.frame_rate, s.bitrate, s.bit_depth,
       s.color_transfer, s.color_primaries, s.hdr_format, s.is_default, s.is_forced,
       s.stream_metadata_json
FROM media_streams s
JOIN media_file_items l ON l.media_file_id = s.media_file_id
WHERE l.media_item_id = ?1
ORDER BY s.media_file_id, s.stream_index;

-- name: ListItemSubtitles :many
SELECT s.id, s.media_file_id, s.relative_path, s.language, s.format, s.is_default, s.is_forced
FROM subtitles s
JOIN media_file_items l ON l.media_file_id = s.media_file_id
WHERE l.media_item_id = ?1
ORDER BY s.media_file_id, s.relative_path;

-- name: CountPendingMetadata :one
SELECT COUNT(*) FROM media_items
WHERE parent_id IS NULL AND metadata_status = 'pending' AND metadata_locked = 0;

-- name: CountRunningScans :one
SELECT COUNT(*) FROM scan_jobs WHERE status = 'running';

-- name: CountLibraries :one
SELECT COUNT(*) FROM libraries;
