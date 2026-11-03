-- name: GetMediaItem :one
SELECT * FROM media_items WHERE id = ?1;

-- name: GetTopLevelItem :one
SELECT id FROM media_items
WHERE library_id = ?1 AND type = ?2 AND match_key = ?3 AND parent_id IS NULL;

-- name: GetSeason :one
SELECT id FROM media_items WHERE parent_id = ?1 AND type = 'season' AND season_number = ?2;

-- name: GetEpisode :one
SELECT id FROM media_items WHERE parent_id = ?1 AND type = 'episode' AND episode_number = ?2;

-- name: CreateMediaItem :exec
INSERT INTO media_items (
    id, library_id, parent_id, type, title, sort_title, year,
    season_number, episode_number, match_key, duration_ms, created_at, updated_at
) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?12);

-- name: SetItemDurationIfEmpty :exec
UPDATE media_items SET duration_ms = ?2 WHERE id = ?1 AND duration_ms IS NULL;

-- name: LinkFileItem :exec
INSERT INTO media_file_items (media_file_id, media_item_id, part_order)
VALUES (?1, ?2, ?3)
ON CONFLICT DO NOTHING;

-- name: DeleteOrphanLeafItems :execrows
DELETE FROM media_items
WHERE type IN ('movie', 'episode')
  AND NOT EXISTS (
    SELECT 1 FROM media_file_items l WHERE l.media_item_id = media_items.id
  );

-- name: DeleteEmptyParents :execrows
DELETE FROM media_items
WHERE media_items.type = ?1
  AND NOT EXISTS (
    SELECT 1 FROM media_items c WHERE c.parent_id = media_items.id
  );

-- name: GetItemAccess :one
SELECT i.library_id, l.enabled,
       CAST(COALESCE(i.min_age, p.min_age, g.min_age, -1) AS INTEGER) AS min_age
FROM media_items i
JOIN libraries l ON l.id = i.library_id
LEFT JOIN media_items p ON p.id = i.parent_id
LEFT JOIN media_items g ON g.id = p.parent_id
WHERE i.id = ?1;
