// Package libraries gestiona las bibliotecas, sus rutas y los permisos por
// biblioteca.
package libraries

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/scanner"
	"medianode/internal/storage"
)

const maxNameLength = 100

// Library es una biblioteca con sus rutas. Paths solo se rellena para los
// administradores: son rutas físicas del servidor.
type Library struct {
	sqlc.Library
	Paths []sqlc.LibraryPath
}

// CreateParams son los datos de una biblioteca nueva.
type CreateParams struct {
	Name        string
	Type        media.LibraryType
	DLNAEnabled bool
	Paths       []string
}

// UpdateParams cambia los campos presentes. El tipo no cambia: los elementos
// ya creados dependen de él.
type UpdateParams struct {
	Name        *string
	Enabled     *bool
	DLNAEnabled *bool
}

// Grant es el permiso de un usuario sobre una biblioteca.
type Grant struct {
	LibraryID   string
	Permissions []media.Permission
}

// SessionCloser cierra las sesiones de reproducción de una biblioteca.
type SessionCloser interface {
	TerminateLibrary(ctx context.Context, libraryID string) error
}

// Service gestiona las bibliotecas.
type Service struct {
	db       *database.DB
	authz    *authorization.Authorizer
	scanner  *scanner.Scanner
	reserved []string

	// Sessions, si no es nil, cierra las sesiones antes de eliminar o
	// desactivar una biblioteca (§6.3).
	Sessions SessionCloser
}

// New crea el servicio. reserved son los directorios del servidor, que ninguna
// biblioteca puede contener ni estar dentro de ellos.
func New(db *database.DB, authz *authorization.Authorizer, sc *scanner.Scanner, reserved []string) *Service {
	return &Service{db: db, authz: authz, scanner: sc, reserved: reserved}
}

// List devuelve las bibliotecas que el actor puede ver.
func (s *Service) List(ctx context.Context, actor authorization.Actor) ([]Library, error) {
	if !authorization.IsAdmin(actor) {
		rows, err := s.db.R.ListVisibleLibraries(ctx, actor.UserID)
		if err != nil {
			return nil, fmt.Errorf("listar bibliotecas: %w", err)
		}
		out := make([]Library, len(rows))
		for i, r := range rows {
			out[i] = Library{Library: r}
		}
		return out, nil
	}
	rows, err := s.db.R.ListLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("listar bibliotecas: %w", err)
	}
	out := make([]Library, len(rows))
	for i, r := range rows {
		paths, err := s.db.R.ListLibraryPaths(ctx, r.ID)
		if err != nil {
			return nil, fmt.Errorf("listar rutas: %w", err)
		}
		out[i] = Library{Library: r, Paths: paths}
	}
	return out, nil
}

// Get devuelve una biblioteca que el actor puede ver.
func (s *Service) Get(ctx context.Context, actor authorization.Actor, id string) (*Library, error) {
	if err := s.authz.CanLibrary(ctx, actor, id, media.PermissionView); err != nil {
		return nil, err
	}
	return s.load(ctx, s.db.R, actor, id)
}

func (s *Service) load(ctx context.Context, q *sqlc.Queries, actor authorization.Actor, id string) (*Library, error) {
	row, err := q.GetLibrary(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer biblioteca: %w", err)
	}
	lib := &Library{Library: row}
	if authorization.IsAdmin(actor) {
		if lib.Paths, err = q.ListLibraryPaths(ctx, id); err != nil {
			return nil, fmt.Errorf("listar rutas: %w", err)
		}
	}
	return lib, nil
}

// Create crea una biblioteca con sus rutas.
func (s *Service) Create(ctx context.Context, actor authorization.Actor, p CreateParams) (*Library, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	name, err := validateName(p.Name)
	if err != nil {
		return nil, err
	}
	if !p.Type.Valid() {
		return nil, core.ErrValidation("El tipo de biblioteca debe ser movies o tv")
	}
	var paths []string
	for _, raw := range p.Paths {
		clean, err := s.validateRoot(raw)
		if err != nil {
			return nil, err
		}
		if err := checkOverlap(clean, paths); err != nil {
			return nil, err
		}
		paths = append(paths, clean)
	}

	id, err := database.NewID()
	if err != nil {
		return nil, err
	}
	var out *Library
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		err := q.CreateLibrary(ctx, sqlc.CreateLibraryParams{
			ID: id, Name: name, Type: string(p.Type), Enabled: 1,
			DlnaEnabled: boolInt(p.DLNAEnabled), CreatedAt: database.Now(),
		})
		if err != nil {
			return fmt.Errorf("crear biblioteca: %w", err)
		}
		for _, path := range paths {
			if err := insertPath(ctx, q, id, path); err != nil {
				return err
			}
		}
		out, err = s.load(ctx, q, actor, id)
		return err
	})
	return out, err
}

// Update cambia nombre, activación o DLNA. Exige el permiso manage.
func (s *Service) Update(ctx context.Context, actor authorization.Actor, id string, p UpdateParams) (*Library, error) {
	if err := s.authz.CanLibrary(ctx, actor, id, media.PermissionManage); err != nil {
		return nil, err
	}
	var name string
	if p.Name != nil {
		var err error
		if name, err = validateName(*p.Name); err != nil {
			return nil, err
		}
	}
	var out *Library
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		row, err := q.GetLibrary(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrNotFound()
		}
		if err != nil {
			return fmt.Errorf("leer biblioteca: %w", err)
		}
		if p.Name != nil {
			row.Name = name
		}
		if p.Enabled != nil {
			row.Enabled = boolInt(*p.Enabled)
		}
		if p.DLNAEnabled != nil {
			row.DlnaEnabled = boolInt(*p.DLNAEnabled)
		}
		err = q.UpdateLibrary(ctx, sqlc.UpdateLibraryParams{
			ID: id, Name: row.Name, Enabled: row.Enabled, DlnaEnabled: row.DlnaEnabled, UpdatedAt: database.Now(),
		})
		if err != nil {
			return fmt.Errorf("actualizar biblioteca: %w", err)
		}
		out, err = s.load(ctx, q, actor, id)
		return err
	})
	if err == nil && p.Enabled != nil && !*p.Enabled {
		err = s.closeSessions(ctx, id)
	}
	return out, err
}

func (s *Service) closeSessions(ctx context.Context, id string) error {
	if s.Sessions == nil {
		return nil
	}
	if err := s.Sessions.TerminateLibrary(ctx, id); err != nil {
		return fmt.Errorf("cerrar sesiones: %w", err)
	}
	return nil
}

// Delete detiene el escaneo en curso y elimina la biblioteca con todo su
// catálogo, después de cerrar sus sesiones de reproducción.
func (s *Service) Delete(ctx context.Context, actor authorization.Actor, id string) error {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return err
	}
	if err := s.authz.CanLibrary(ctx, actor, id, media.PermissionView); err != nil {
		return err
	}
	if err := s.closeSessions(ctx, id); err != nil {
		return err
	}
	return s.scanner.Exclusive(id, func() error {
		return s.db.InTx(ctx, func(q *sqlc.Queries) error {
			if err := q.DeleteLibrary(ctx, id); err != nil {
				return fmt.Errorf("eliminar biblioteca: %w", err)
			}
			return nil
		})
	})
}

// AddPath añade una raíz a la biblioteca.
func (s *Service) AddPath(ctx context.Context, actor authorization.Actor, id, path string) (*sqlc.LibraryPath, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	clean, err := s.validateRoot(path)
	if err != nil {
		return nil, err
	}
	var out *sqlc.LibraryPath
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		if _, err := q.GetLibrary(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return core.ErrNotFound()
		} else if err != nil {
			return fmt.Errorf("leer biblioteca: %w", err)
		}
		existing, err := q.ListLibraryPaths(ctx, id)
		if err != nil {
			return fmt.Errorf("listar rutas: %w", err)
		}
		others := make([]string, len(existing))
		for i, e := range existing {
			others[i] = e.Path
		}
		if err := checkOverlap(clean, others); err != nil {
			return err
		}
		pathID, err := database.NewID()
		if err != nil {
			return err
		}
		if err := q.CreateLibraryPath(ctx, sqlc.CreateLibraryPathParams{ID: pathID, LibraryID: id, Path: clean}); err != nil {
			return fmt.Errorf("crear ruta: %w", err)
		}
		row, err := q.GetLibraryPath(ctx, sqlc.GetLibraryPathParams{ID: pathID, LibraryID: id})
		out = &row
		return err
	})
	return out, err
}

// RemovePath quita una raíz. Sus archivos se borran de inmediato, sin
// retención: es una decisión explícita del administrador.
func (s *Service) RemovePath(ctx context.Context, actor authorization.Actor, id, pathID string) error {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return err
	}
	return s.scanner.Exclusive(id, func() error {
		return s.db.InTx(ctx, func(q *sqlc.Queries) error {
			if _, err := q.GetLibraryPath(ctx, sqlc.GetLibraryPathParams{ID: pathID, LibraryID: id}); errors.Is(err, sql.ErrNoRows) {
				return core.ErrNotFound()
			} else if err != nil {
				return fmt.Errorf("leer ruta: %w", err)
			}
			if err := q.DeleteLibraryPath(ctx, pathID); err != nil {
				return fmt.Errorf("quitar ruta: %w", err)
			}
			_, err := scanner.DeleteOrphansTx(ctx, q)
			return err
		})
	})
}

// StartScan inicia un escaneo manual, o devuelve el que ya está en curso.
func (s *Service) StartScan(ctx context.Context, actor authorization.Actor, id string) (*sqlc.ScanJob, error) {
	if err := s.authz.CanLibrary(ctx, actor, id, media.PermissionScan); err != nil {
		return nil, err
	}
	job, err := s.scanner.Start(id, media.TriggerManual)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ScanStatus devuelve el último escaneo de la biblioteca, o nil si nunca se
// escaneó.
func (s *Service) ScanStatus(ctx context.Context, actor authorization.Actor, id string) (*sqlc.ScanJob, error) {
	if err := s.authz.CanLibrary(ctx, actor, id, media.PermissionView); err != nil {
		return nil, err
	}
	job, err := s.db.R.GetLatestScanJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leer escaneo: %w", err)
	}
	return &job, nil
}

// Permissions devuelve los permisos por biblioteca de un usuario.
func (s *Service) Permissions(ctx context.Context, actor authorization.Actor, userID string) ([]Grant, error) {
	if _, err := s.manageable(ctx, s.db.R, actor, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.R.ListUserPermissions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("leer permisos: %w", err)
	}
	var out []Grant
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].LibraryID != r.LibraryID {
			out = append(out, Grant{LibraryID: r.LibraryID})
		}
		last := &out[len(out)-1]
		last.Permissions = append(last.Permissions, media.Permission(r.Permission))
	}
	return out, nil
}

// SetPermissions sustituye todos los permisos del usuario. Owner y
// administradores acceden a todo y no tienen permisos explícitos.
func (s *Service) SetPermissions(ctx context.Context, actor authorization.Actor, userID string, grants []Grant) ([]Grant, error) {
	seen := map[string]bool{}
	for _, g := range grants {
		if seen[g.LibraryID] {
			return nil, core.ErrValidation("Una biblioteca aparece dos veces")
		}
		seen[g.LibraryID] = true
		for _, p := range g.Permissions {
			if !p.Valid() {
				return nil, core.ErrValidation("Permiso desconocido: debe ser view, scan o manage")
			}
		}
	}
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		target, err := s.manageable(ctx, q, actor, userID)
		if err != nil {
			return err
		}
		if authorization.IsAdmin(authorization.Actor{Role: media.Role(target.Role)}) {
			return core.ErrValidation("Los administradores acceden a todas las bibliotecas")
		}
		if err := q.DeleteUserPermissions(ctx, userID); err != nil {
			return fmt.Errorf("borrar permisos: %w", err)
		}
		for _, g := range grants {
			if _, err := q.GetLibrary(ctx, g.LibraryID); errors.Is(err, sql.ErrNoRows) {
				return core.ErrValidation("Una de las bibliotecas no existe")
			} else if err != nil {
				return fmt.Errorf("leer biblioteca: %w", err)
			}
			done := map[media.Permission]bool{}
			for _, p := range g.Permissions {
				if done[p] {
					continue
				}
				done[p] = true
				err := q.AddUserPermission(ctx, sqlc.AddUserPermissionParams{
					UserID: userID, LibraryID: g.LibraryID, Permission: string(p),
				})
				if err != nil {
					return fmt.Errorf("guardar permiso: %w", err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Permissions(ctx, actor, userID)
}

// manageable devuelve el usuario si el actor puede administrarlo.
func (s *Service) manageable(ctx context.Context, q *sqlc.Queries, actor authorization.Actor, userID string) (*sqlc.User, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	user, err := q.GetUser(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer usuario: %w", err)
	}
	if !authorization.CanManage(actor, media.Role(user.Role)) {
		return nil, core.ErrForbidden()
	}
	return &user, nil
}

func (s *Service) validateRoot(path string) (string, error) {
	clean, err := storage.ValidateLibraryRoot(strings.TrimSpace(path), s.reserved)
	switch {
	case errors.Is(err, storage.ErrRootNotAbsolute):
		return "", core.ErrPathUnavailable("La ruta debe ser absoluta")
	case errors.Is(err, storage.ErrRootReserved):
		return "", core.ErrPathUnavailable("La ruta se solapa con los directorios del servidor")
	case err != nil:
		return "", core.ErrPathUnavailable("La ruta no existe, no es una carpeta o el servidor no puede leerla")
	}
	return clean, nil
}

// checkOverlap impide rutas anidadas en una misma biblioteca: indexarían dos
// veces los mismos archivos.
func checkOverlap(path string, others []string) error {
	for _, o := range others {
		if storage.Overlaps(path, o) {
			return core.ErrValidation("La ruta coincide o se solapa con otra de la biblioteca")
		}
	}
	return nil
}

func insertPath(ctx context.Context, q *sqlc.Queries, libraryID, path string) error {
	id, err := database.NewID()
	if err != nil {
		return err
	}
	if err := q.CreateLibraryPath(ctx, sqlc.CreateLibraryPathParams{ID: id, LibraryID: libraryID, Path: path}); err != nil {
		return fmt.Errorf("crear ruta: %w", err)
	}
	return nil
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLength {
		return "", core.ErrValidation("El nombre de la biblioteca no es válido")
	}
	return name, nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
