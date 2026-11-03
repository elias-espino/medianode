// Package authorization decide qué puede hacer cada actor.
package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

// Actor identifica a quien realiza una petición. El rol sale de la base en cada
// petición, por lo que un cambio de rol surte efecto de inmediato.
type Actor struct {
	UserID       string
	DeviceID     string
	Role         media.Role
	MaxAgeRating *int64
}

type contextKey struct{}

// WithActor guarda el actor autenticado en el contexto.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, contextKey{}, a)
}

// FromContext devuelve el actor autenticado.
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(contextKey{}).(Actor)
	return a, ok
}

func rank(r media.Role) int {
	switch r {
	case media.RoleOwner:
		return 4
	case media.RoleAdmin:
		return 3
	case media.RoleUser:
		return 2
	case media.RoleRestricted:
		return 1
	}
	return 0
}

// RequireRole exige que el actor tenga al menos el rol min.
func RequireRole(actor Actor, min media.Role) error {
	if rank(actor.Role) < rank(min) {
		return core.ErrForbidden()
	}
	return nil
}

// CanManage indica si el actor gestiona a usuarios de ese rol. Hace falta un
// rango superior: un administrador no actúa sobre otro ni sobre el Owner.
func CanManage(actor Actor, target media.Role) bool {
	return rank(actor.Role) > rank(target)
}

// IsAdmin indica si el actor accede a todas las bibliotecas sin permisos
// explícitos.
func IsAdmin(actor Actor) bool {
	return rank(actor.Role) >= rank(media.RoleAdmin)
}

func permissionRank(p media.Permission) int {
	switch p {
	case media.PermissionManage:
		return 3
	case media.PermissionScan:
		return 2
	case media.PermissionView:
		return 1
	}
	return 0
}

// Authorizer comprueba el acceso a recursos concretos. Cada comprobación lee
// la base, por lo que un permiso retirado surte efecto en la siguiente
// petición.
type Authorizer struct {
	db *database.DB
}

// NewAuthorizer crea el comprobador.
func NewAuthorizer(db *database.DB) *Authorizer {
	return &Authorizer{db: db}
}

// CanLibrary exige el permiso p sobre la biblioteca. Si el actor ni siquiera
// la ve, responde 404, para no revelar que existe; si la ve pero le falta el
// permiso, PERMISSION_LIBRARY_DENIED.
func (a *Authorizer) CanLibrary(ctx context.Context, actor Actor, libraryID string, p media.Permission) error {
	lib, err := a.db.R.GetLibrary(ctx, libraryID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound()
	}
	if err != nil {
		return fmt.Errorf("leer biblioteca: %w", err)
	}
	if IsAdmin(actor) {
		return nil
	}
	if lib.Enabled == 0 {
		return core.ErrNotFound()
	}
	return a.checkPermission(ctx, actor, libraryID, p)
}

func (a *Authorizer) checkPermission(ctx context.Context, actor Actor, libraryID string, p media.Permission) error {
	perms, err := a.db.R.ListUserLibraryPermissions(ctx, sqlc.ListUserLibraryPermissionsParams{
		UserID: actor.UserID, LibraryID: libraryID,
	})
	if err != nil {
		return fmt.Errorf("leer permisos: %w", err)
	}
	best := 0
	for _, granted := range perms {
		best = max(best, permissionRank(media.Permission(granted)))
	}
	if best == 0 {
		return core.ErrNotFound()
	}
	if best < permissionRank(p) {
		return core.ErrLibraryDenied()
	}
	return nil
}

// CanItem exige poder ver el elemento: ver su biblioteca y, para un usuario
// restringido, que su clasificación lo permita (§15.1). Responde 404 en
// cualquier caso negativo.
func (a *Authorizer) CanItem(ctx context.Context, actor Actor, itemID string) error {
	acc, err := a.db.R.GetItemAccess(ctx, itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound()
	}
	if err != nil {
		return fmt.Errorf("leer elemento: %w", err)
	}
	return a.itemAllowed(ctx, actor, acc.LibraryID, acc.Enabled, acc.MinAge)
}

func (a *Authorizer) itemAllowed(ctx context.Context, actor Actor, libraryID string, enabled, minAge int64) error {
	if IsAdmin(actor) {
		return nil
	}
	if enabled == 0 {
		return core.ErrNotFound()
	}
	if err := a.checkPermission(ctx, actor, libraryID, media.PermissionView); err != nil {
		return notFound(err)
	}
	if !AgeAllowed(actor, minAge) {
		return core.ErrNotFound()
	}
	return nil
}

// AgeAllowed indica si un usuario restringido puede ver contenido con esa edad
// mínima. -1 significa desconocida, y lo desconocido queda oculto. Sin
// max_age_rating, un restringido solo ve contenido para todos los públicos.
func AgeAllowed(actor Actor, minAge int64) bool {
	if actor.Role != media.RoleRestricted {
		return true
	}
	limit := int64(0)
	if actor.MaxAgeRating != nil {
		limit = *actor.MaxAgeRating
	}
	return minAge >= 0 && minAge <= limit
}

// CanFile exige poder ver todos los elementos del archivo. Un archivo sin
// identificar solo lo ven los administradores.
func (a *Authorizer) CanFile(ctx context.Context, actor Actor, fileID string) error {
	acc, err := a.db.R.GetFileAccess(ctx, fileID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound()
	}
	if err != nil {
		return fmt.Errorf("leer archivo: %w", err)
	}
	if IsAdmin(actor) {
		return nil
	}
	items, err := a.db.R.ListFileItemIDs(ctx, fileID)
	if err != nil {
		return fmt.Errorf("leer elementos del archivo: %w", err)
	}
	if len(items) == 0 || acc.Enabled == 0 {
		return core.ErrNotFound()
	}
	for _, id := range items {
		if err := a.CanItem(ctx, actor, id); err != nil {
			return err
		}
	}
	return nil
}

// notFound convierte la falta de permiso en 404: quien no puede ver la
// biblioteca no debe saber que el recurso existe.
func notFound(err error) error {
	if code := core.CodeOf(err); code == core.CodeLibraryDenied || code == core.CodeNotFound {
		return core.ErrNotFound()
	}
	return err
}
