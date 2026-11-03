-- name: CreateScanJob :exec
INSERT INTO scan_jobs (id, library_id, trigger, status, started_at)
VALUES (?1, ?2, ?3, 'running', ?4);

-- name: GetScanJob :one
SELECT * FROM scan_jobs WHERE id = ?1;

-- name: GetRunningScanJob :one
SELECT * FROM scan_jobs WHERE library_id = ?1 AND status = 'running';

-- name: GetLatestScanJob :one
SELECT * FROM scan_jobs WHERE library_id = ?1 ORDER BY started_at DESC, id DESC LIMIT 1;

-- name: UpdateScanProgress :exec
UPDATE scan_jobs SET files_discovered = ?2, files_processed = ?3 WHERE id = ?1;

-- name: FinishScanJob :exec
UPDATE scan_jobs SET
    status = ?2,
    finished_at = ?3,
    files_discovered = ?4,
    files_processed = ?5,
    error_message = ?6
WHERE id = ?1 AND status = 'running';

-- name: InterruptRunningScanJobs :execrows
UPDATE scan_jobs SET status = 'interrupted', finished_at = ?1 WHERE status = 'running';
