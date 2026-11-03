-- name: ListPathFiles :many
SELECT id, relative_path, file_size, modified_at, probe_status, missing_since
FROM media_files WHERE library_path_id = ?1;

-- name: GetMediaFile :one
SELECT * FROM media_files WHERE id = ?1;

-- name: CreateMediaFile :exec
INSERT INTO media_files (
    id, library_path_id, relative_path, file_size, modified_at, file_hash,
    container, duration_ms, bitrate, probe_status, created_at, updated_at
) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?11);

-- name: UpdateMediaFileProbe :exec
UPDATE media_files SET
    file_size = ?2,
    modified_at = ?3,
    file_hash = ?4,
    container = ?5,
    duration_ms = ?6,
    bitrate = ?7,
    probe_status = ?8,
    missing_since = NULL,
    updated_at = ?9
WHERE id = ?1;

-- name: MarkFileMissing :exec
UPDATE media_files SET missing_since = ?2, updated_at = ?2 WHERE id = ?1 AND missing_since IS NULL;

-- name: ClearFileMissing :exec
UPDATE media_files SET missing_since = NULL, updated_at = ?2 WHERE id = ?1;

-- name: FindMoveCandidate :one
SELECT f.id FROM media_files f
JOIN library_paths lp ON lp.id = f.library_path_id
WHERE lp.library_id = ?1
  AND f.missing_since IS NOT NULL
  AND f.file_size = ?2
  AND f.file_hash = ?3
ORDER BY f.missing_since, f.id
LIMIT 1;

-- name: MoveMediaFile :exec
UPDATE media_files SET
    library_path_id = ?2,
    relative_path = ?3,
    modified_at = ?4,
    missing_since = NULL,
    updated_at = ?5
WHERE id = ?1;

-- name: DeleteExpiredMissingFiles :execrows
DELETE FROM media_files WHERE missing_since IS NOT NULL AND missing_since < ?1;

-- name: DeleteFileStreams :exec
DELETE FROM media_streams WHERE media_file_id = ?1;

-- name: CreateMediaStream :exec
INSERT INTO media_streams (
    id, media_file_id, stream_index, stream_type, codec, profile, level, language,
    channels, width, height, frame_rate, bitrate, bit_depth, color_transfer,
    color_primaries, hdr_format, is_default, is_forced, stream_metadata_json
) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20);

-- name: ListFileStreams :many
SELECT id, media_file_id, stream_index, stream_type, codec, profile, level, language,
       channels, width, height, frame_rate, bitrate, bit_depth, color_transfer,
       color_primaries, hdr_format, is_default, is_forced, stream_metadata_json
FROM media_streams WHERE media_file_id = ?1 ORDER BY stream_index;

-- name: ListPathSubtitles :many
SELECT s.id, s.media_file_id, s.relative_path
FROM subtitles s
JOIN media_files f ON f.id = s.media_file_id
WHERE f.library_path_id = ?1;

-- name: CreateSubtitle :exec
INSERT INTO subtitles (id, media_file_id, relative_path, language, format, is_default, is_forced)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7);

-- name: DeleteSubtitle :exec
DELETE FROM subtitles WHERE id = ?1;

-- name: ListFileSubtitles :many
SELECT id, media_file_id, relative_path, language, format, is_default, is_forced
FROM subtitles WHERE media_file_id = ?1 ORDER BY relative_path;

-- name: ListUnidentifiedFiles :many
SELECT f.id, f.relative_path, f.duration_ms
FROM media_files f
JOIN library_paths lp ON lp.id = f.library_path_id
WHERE lp.library_id = ?1
  AND f.probe_status = 'done'
  AND f.missing_since IS NULL
  AND NOT EXISTS (SELECT 1 FROM media_file_items l WHERE l.media_file_id = f.id)
ORDER BY f.relative_path;

-- name: GetFileAccess :one
SELECT lp.library_id, l.enabled
FROM media_files f
JOIN library_paths lp ON lp.id = f.library_path_id
JOIN libraries l ON l.id = lp.library_id
WHERE f.id = ?1;

-- name: ListFileItemIDs :many
SELECT media_item_id FROM media_file_items WHERE media_file_id = ?1 ORDER BY part_order;
