-- Write queries for metadata enrichment. Comments must stay ASCII.

-- name: ListPendingMetadata :many
SELECT id, library_id, type, title, year, original_language
FROM media_items
WHERE parent_id IS NULL AND metadata_status = 'pending' AND metadata_locked = 0
ORDER BY created_at, id
LIMIT ?1;

-- name: ListMissingSortTitles :many
SELECT id, title, original_language FROM media_items
WHERE sort_title IS NULL AND parent_id IS NULL
LIMIT ?1;

-- name: SetSortTitle :exec
UPDATE media_items SET sort_title = ?2 WHERE id = ?1;

-- name: SetMetadataStatus :exec
UPDATE media_items SET metadata_status = ?2, updated_at = ?3 WHERE id = ?1;

-- name: SetMetadataLocked :exec
UPDATE media_items SET metadata_locked = ?2, updated_at = ?3 WHERE id = ?1;

-- name: ApplyItemMetadata :execrows
UPDATE media_items SET
    title = ?2,
    original_title = ?3,
    sort_title = ?4,
    year = ?5,
    release_date = ?6,
    overview = ?7,
    age_rating = ?8,
    min_age = ?9,
    original_language = ?10,
    metadata_status = 'done',
    updated_at = ?11
WHERE id = ?1 AND metadata_locked = 0;

-- name: EditItemMetadata :exec
UPDATE media_items SET
    title = ?2,
    original_title = ?3,
    sort_title = ?4,
    year = ?5,
    release_date = ?6,
    overview = ?7,
    age_rating = ?8,
    min_age = ?9,
    metadata_locked = ?10,
    updated_at = ?11
WHERE id = ?1;

-- name: GetExternalID :one
SELECT external_id FROM media_external_ids WHERE media_item_id = ?1 AND provider = ?2;

-- name: SetExternalID :exec
INSERT INTO media_external_ids (media_item_id, provider, external_id)
VALUES (?1, ?2, ?3)
ON CONFLICT (media_item_id, provider) DO UPDATE SET external_id = excluded.external_id;

-- name: DeleteItemExternalIDs :exec
DELETE FROM media_external_ids WHERE media_item_id = ?1;

-- name: GetGenreByName :one
SELECT id FROM genres WHERE name = ?1;

-- name: CreateGenre :exec
INSERT INTO genres (id, name) VALUES (?1, ?2);

-- name: DeleteItemGenres :exec
DELETE FROM media_item_genres WHERE media_item_id = ?1;

-- name: AddItemGenre :exec
INSERT INTO media_item_genres (media_item_id, genre_id) VALUES (?1, ?2)
ON CONFLICT DO NOTHING;

-- name: UpsertPerson :exec
INSERT INTO people (id, name) VALUES (?1, ?2)
ON CONFLICT (id) DO UPDATE SET name = excluded.name;

-- name: DeleteItemPeople :exec
DELETE FROM media_item_people WHERE media_item_id = ?1;

-- name: AddItemPerson :exec
INSERT INTO media_item_people (media_item_id, person_id, role, character_name, sort_order)
VALUES (?1, ?2, ?3, ?4, ?5)
ON CONFLICT DO NOTHING;

-- name: DeleteOrphanPeople :execrows
DELETE FROM people
WHERE NOT EXISTS (SELECT 1 FROM media_item_people mp WHERE mp.person_id = people.id);

-- name: DeleteOrphanGenres :execrows
DELETE FROM genres
WHERE NOT EXISTS (SELECT 1 FROM media_item_genres mg WHERE mg.genre_id = genres.id);

-- name: UpsertImage :exec
-- A new source_url invalidates the downloaded file; the same URL keeps it.
INSERT INTO media_images (id, media_item_id, type, source_url)
VALUES (?1, ?2, ?3, ?4)
ON CONFLICT (media_item_id, type) DO UPDATE SET
    local_path = CASE WHEN media_images.source_url IS excluded.source_url
                      THEN media_images.local_path ELSE NULL END,
    source_url = excluded.source_url;

-- name: DeleteItemImage :exec
DELETE FROM media_images WHERE media_item_id = ?1 AND type = ?2;

-- name: SetImageLocalPath :execrows
UPDATE media_images SET local_path = ?3
WHERE media_item_id = ?1 AND type = ?2 AND source_url = ?4;

-- name: ListImagesToFetch :many
SELECT media_item_id, type, source_url FROM media_images
WHERE local_path IS NULL AND source_url IS NOT NULL
ORDER BY media_item_id, type
LIMIT ?1;

-- name: ListImageLocalPaths :many
SELECT local_path FROM media_images WHERE local_path IS NOT NULL;

-- name: ListStaleMetadata :many
SELECT id FROM media_items
WHERE parent_id IS NULL AND metadata_status = 'done' AND metadata_locked = 0 AND updated_at < ?1
ORDER BY updated_at
LIMIT ?2;

-- name: ClearTreeImages :exec
UPDATE media_images SET local_path = NULL
WHERE media_item_id = ?1
   OR media_item_id IN (SELECT c.id FROM media_items c WHERE c.parent_id = ?1)
   OR media_item_id IN (
     SELECT e.id FROM media_items e JOIN media_items s ON s.id = e.parent_id WHERE s.parent_id = ?1
   );

-- name: ListChildNumbers :many
SELECT id, season_number, episode_number FROM media_items
WHERE parent_id = ?1
ORDER BY season_number, episode_number;

-- name: RequeueSeriesMetadata :exec
UPDATE media_items SET metadata_status = 'pending', updated_at = ?2
WHERE id = ?1 AND metadata_status = 'done' AND metadata_locked = 0;

-- name: FindSameExternalItem :one
SELECT m.id, m.created_at, m.match_key
FROM media_items m
JOIN media_external_ids x ON x.media_item_id = m.id
WHERE x.provider = ?1 AND x.external_id = ?2 AND m.library_id = ?3 AND m.type = ?4
  AND m.parent_id IS NULL AND m.id <> ?5
ORDER BY m.created_at, m.id
LIMIT 1;

-- name: ReparentItem :exec
UPDATE media_items SET parent_id = ?2, updated_at = ?3 WHERE id = ?1;

-- name: MoveFileLinks :exec
UPDATE OR IGNORE media_file_items SET media_item_id = ?2 WHERE media_item_id = ?1;

-- name: DeleteItemByID :exec
DELETE FROM media_items WHERE id = ?1;

-- name: SetMatchKey :exec
UPDATE OR IGNORE media_items SET match_key = ?2 WHERE id = ?1;
