-- name: CreateLibrary :exec
INSERT INTO libraries (id, name, type, enabled, dlna_enabled, created_at, updated_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?6);

-- name: GetLibrary :one
SELECT * FROM libraries WHERE id = ?1;

-- name: ListLibraries :many
SELECT * FROM libraries ORDER BY name COLLATE NOCASE, id;

-- Cualquier permiso implica ver la biblioteca. Una biblioteca desactivada solo
-- la ven los administradores.
-- name: ListVisibleLibraries :many
SELECT * FROM libraries l
WHERE l.enabled = 1
  AND EXISTS (
    SELECT 1 FROM user_permissions up
    WHERE up.library_id = l.id AND up.user_id = ?1
  )
ORDER BY l.name COLLATE NOCASE, l.id;

-- name: ListEnabledLibraryIDs :many
SELECT id FROM libraries WHERE enabled = 1 ORDER BY id;

-- name: UpdateLibrary :exec
UPDATE libraries SET name = ?2, enabled = ?3, dlna_enabled = ?4, updated_at = ?5 WHERE id = ?1;

-- name: DeleteLibrary :exec
DELETE FROM libraries WHERE id = ?1;

-- name: CreateLibraryPath :exec
INSERT INTO library_paths (id, library_id, path) VALUES (?1, ?2, ?3);

-- name: GetLibraryPath :one
SELECT * FROM library_paths WHERE id = ?1 AND library_id = ?2;

-- name: ListLibraryPaths :many
SELECT * FROM library_paths WHERE library_id = ?1 ORDER BY path, id;

-- name: ListEnabledLibraryPaths :many
SELECT lp.* FROM library_paths lp
JOIN libraries l ON l.id = lp.library_id
WHERE l.enabled = 1
ORDER BY lp.library_id, lp.path;

-- name: DeleteLibraryPath :exec
DELETE FROM library_paths WHERE id = ?1;

-- name: SetLibraryPathAvailable :exec
UPDATE library_paths SET is_available = ?2 WHERE id = ?1;

-- name: SetLibraryPathScanned :exec
UPDATE library_paths SET last_scan_at = ?2 WHERE id = ?1;

-- name: ListUserLibraryPermissions :many
SELECT permission FROM user_permissions WHERE user_id = ?1 AND library_id = ?2;

-- name: ListUserPermissions :many
SELECT library_id, permission FROM user_permissions WHERE user_id = ?1 ORDER BY library_id, permission;

-- name: DeleteUserPermissions :exec
DELETE FROM user_permissions WHERE user_id = ?1;

-- name: AddUserPermission :exec
INSERT INTO user_permissions (user_id, library_id, permission) VALUES (?1, ?2, ?3);
