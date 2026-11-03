-- Playback sessions, progress and history. Comments must stay ASCII.

-- name: CreateSession :exec
INSERT INTO playback_sessions (
    id, client_kind, user_id, device_id, media_item_id, media_file_id,
    selected_audio_stream_id, selected_subtitle_id, playback_mode, status,
    start_position_ms, position_ms, duration_ms, started_at, last_activity_at, client_ip
) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?11, ?12, ?13, ?13, ?14);

-- name: GetSession :one
SELECT * FROM playback_sessions WHERE id = ?1;

-- name: SetSessionStatus :exec
UPDATE playback_sessions SET status = ?2 WHERE id = ?1 AND ended_at IS NULL;

-- name: FlushSession :execrows
UPDATE playback_sessions SET
    position_ms = ?2,
    last_heartbeat_at = ?3,
    last_activity_at = ?4
WHERE id = ?1 AND ended_at IS NULL;

-- name: EndSession :execrows
UPDATE playback_sessions SET
    status = ?2,
    position_ms = ?3,
    last_heartbeat_at = ?4,
    last_activity_at = ?5,
    ended_at = ?6
WHERE id = ?1 AND ended_at IS NULL;

-- name: ListOpenSessions :many
SELECT * FROM playback_sessions WHERE ended_at IS NULL;

-- name: GetSessionTarget :one
SELECT mi.library_id, mi.title, mi.duration_ms AS item_duration_ms, f.duration_ms AS file_duration_ms
FROM media_items mi
JOIN media_file_items l ON l.media_item_id = mi.id
JOIN media_files f ON f.id = l.media_file_id
WHERE mi.id = ?1 AND f.id = ?2;

-- name: UpsertProgress :execrows
INSERT INTO playback_progress
    (user_id, media_item_id, position_ms, duration_ms, completed, updated_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6)
ON CONFLICT (user_id, media_item_id) DO UPDATE SET
    position_ms = excluded.position_ms,
    duration_ms = excluded.duration_ms,
    completed = excluded.completed,
    updated_at = excluded.updated_at
WHERE excluded.updated_at >= playback_progress.updated_at;

-- name: GetProgress :one
SELECT * FROM playback_progress WHERE user_id = ?1 AND media_item_id = ?2;

-- name: ListContinueWatching :many
-- Any library permission includes view. Episodes inherit the age of their
-- season or series, as in authorization. No BETWEEN: sqlc numbers its
-- parameter but leaves it out of the generated struct.
SELECT mi.id, mi.type, mi.title, mi.year, mi.parent_id, mi.library_id,
       mi.season_number, mi.episode_number,
       g.id AS series_id, g.title AS series_title,
       pp.position_ms, pp.duration_ms, pp.updated_at,
       CAST(EXISTS (
         SELECT 1 FROM media_images im
         WHERE im.media_item_id = mi.id AND im.local_path IS NOT NULL
       ) AS INTEGER) AS has_image
FROM playback_progress pp
JOIN media_items mi ON mi.id = pp.media_item_id
JOIN libraries l ON l.id = mi.library_id
LEFT JOIN media_items p ON p.id = mi.parent_id
LEFT JOIN media_items g ON g.id = p.parent_id
WHERE pp.user_id = sqlc.arg(user_id)
  AND pp.completed = 0
  AND pp.position_ms > 0
  AND (CAST(sqlc.arg(admin) AS INTEGER) = 1 OR (l.enabled = 1 AND EXISTS (
    SELECT 1 FROM user_permissions up
    WHERE up.user_id = pp.user_id AND up.library_id = mi.library_id
  )))
  AND (CAST(sqlc.arg(restricted) AS INTEGER) = 0
       OR (COALESCE(mi.min_age, p.min_age, g.min_age, -1) >= 0
           AND COALESCE(mi.min_age, p.min_age, g.min_age, -1) <= sqlc.arg(max_age)))
ORDER BY pp.updated_at DESC, mi.id
LIMIT sqlc.arg(page_size);

-- name: AddHistory :exec
INSERT INTO playback_history (id, user_id, device_id, media_item_id, title, position_ms, completed, watched_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8);

-- name: ListHistory :many
SELECT h.id, h.device_id, h.media_item_id, h.title, h.position_ms, h.completed, h.watched_at
FROM playback_history h
WHERE h.user_id = sqlc.arg(user_id)
  AND (h.watched_at, h.id) < (sqlc.arg(before_time), sqlc.arg(before_id))
ORDER BY h.watched_at DESC, h.id DESC
LIMIT sqlc.arg(page_size);

-- name: GetFileLocation :one
SELECT lp.library_id, lp.path AS root, f.relative_path, f.container, f.missing_since, f.probe_status
FROM media_files f
JOIN library_paths lp ON lp.id = f.library_path_id
WHERE f.id = ?1;

-- name: GetExternalSubtitle :one
SELECT s.id, s.media_file_id, s.relative_path, s.format, lp.path AS root
FROM subtitles s
JOIN media_files f ON f.id = s.media_file_id
JOIN library_paths lp ON lp.id = f.library_path_id
WHERE s.id = ?1;

-- name: CreateTranscodeJob :exec
INSERT INTO transcode_jobs (id, playback_session_id, job_kind, status, input_file_id, output_profile, start_position_ms, started_at)
VALUES (?1, ?2, ?3, 'running', ?4, ?5, ?6, ?7);

-- name: FinishTranscodeJob :exec
UPDATE transcode_jobs SET
    status = ?2,
    progress = ?3,
    finished_at = ?4,
    error_code = ?5,
    error_message = ?6
WHERE id = ?1;

-- name: FailActiveTranscodes :execrows
UPDATE transcode_jobs SET
    status = 'failed',
    error_code = 'SERVER_RESTART',
    finished_at = ?1
WHERE status IN ('starting', 'running');
