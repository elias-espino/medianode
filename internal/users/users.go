// Package users gestiona las cuentas y sus preferencias.
package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/devices"
	"medianode/internal/media"
)

const maxLanguageLength = 35 // Longitud máxima práctica de una etiqueta BCP 47.

// CreateParams son los datos de un usuario nuevo.
type CreateParams struct {
	Username     string
	Password     string
	Role         media.Role
	MaxAgeRating *int64
}

// UpdateParams indica los campos que cambian. Un puntero nulo los conserva.
type UpdateParams struct {
	Username       *string
	Password       *string
	Role           *media.Role
	IsActive       *bool
	MaxAgeRating   *int64
	ClearAgeRating bool
}

// Preferences son las preferencias de reproducción.
type Preferences struct {
	AudioLanguage    *string
	SubtitleLanguage *string
	SubtitleMode     media.SubtitleMode
	MaxBitrateRemote *int64
}

// SessionCloser cierra las sesiones de reproducción de un usuario.
type SessionCloser interface {
	TerminateUser(ctx context.Context, userID string) error
}

// Service gestiona los usuarios.
type Service struct {
	db     *database.DB
	hasher *auth.Hasher

	// Sessions, si no es nil, cierra las sesiones del usuario al eliminarlo,
	// desactivarlo o cambiar su contraseña, que revoca sus dispositivos.
	Sessions SessionCloser
}

// New crea el servicio.
func New(db *database.DB, hasher *auth.Hasher) *Service {
	return &Service{db: db, hasher: hasher}
}

// List devuelve todos los usuarios.
func (s *Service) List(ctx context.Context, actor authorization.Actor) ([]sqlc.User, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	rows, err := s.db.R.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listar usuarios: %w", err)
	}
	return rows, nil
}

// Get devuelve un usuario.
func (s *Service) Get(ctx context.Context, actor authorization.Actor, id string) (*sqlc.User, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	return load(ctx, s.db.R, id)
}

// Create crea un usuario de rango inferior al del actor.
func (s *Service) Create(ctx context.Context, actor authorization.Actor, p CreateParams) (*sqlc.User, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	username, err := auth.ValidateCredentials(p.Username, p.Password)
	if err != nil {
		return nil, err
	}
	if err := checkAssignable(actor, p.Role); err != nil {
		return nil, err
	}
	if err := validateAgeRating(p.MaxAgeRating); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(ctx, p.Password)
	if err != nil {
		return nil, err
	}
	id, err := database.NewID()
	if err != nil {
		return nil, err
	}

	var user *sqlc.User
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		if err := checkUsernameFree(ctx, q, username, ""); err != nil {
			return err
		}
		err := q.CreateUser(ctx, sqlc.CreateUserParams{
			ID: id, Username: username, PasswordHash: hash, Role: string(p.Role), MaxAgeRating: p.MaxAgeRating,
		})
		if err != nil {
			return fmt.Errorf("crear usuario: %w", err)
		}
		if err := q.CreatePreferences(ctx, id); err != nil {
			return fmt.Errorf("crear preferencias: %w", err)
		}
		user, err = load(ctx, q, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// Update modifica un usuario de rango inferior o, con límites, al propio actor:
// nadie cambia su rol ni se desactiva, y su contraseña cambia por
// /users/me/password, que exige la actual.
func (s *Service) Update(ctx context.Context, actor authorization.Actor, id string, p UpdateParams) (*sqlc.User, error) {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	self := id == actor.UserID
	if self && (p.Role != nil || p.IsActive != nil || p.Password != nil) {
		return nil, core.ErrForbidden()
	}

	var username string
	var err error
	if p.Username != nil {
		if username, err = auth.ValidateUsername(*p.Username); err != nil {
			return nil, err
		}
	}
	if p.Role != nil {
		if err := checkAssignable(actor, *p.Role); err != nil {
			return nil, err
		}
	}
	if err := validateAgeRating(p.MaxAgeRating); err != nil {
		return nil, err
	}
	var hash string
	if p.Password != nil {
		if err := auth.ValidatePassword(*p.Password); err != nil {
			return nil, err
		}
		if hash, err = s.hasher.Hash(ctx, *p.Password); err != nil {
			return nil, err
		}
	}

	var user *sqlc.User
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		row, err := load(ctx, q, id)
		if err != nil {
			return err
		}
		if !self && !authorization.CanManage(actor, media.Role(row.Role)) {
			return core.ErrForbidden()
		}

		now := database.Now()
		params := sqlc.UpdateUserParams{
			ID: id, Username: row.Username, Role: row.Role, IsActive: row.IsActive,
			MaxAgeRating: row.MaxAgeRating, UpdatedAt: now,
		}
		if p.Username != nil {
			if err := checkUsernameFree(ctx, q, username, id); err != nil {
				return err
			}
			params.Username = username
		}
		if p.Role != nil {
			params.Role = string(*p.Role)
		}
		if p.IsActive != nil {
			params.IsActive = boolToInt(*p.IsActive)
		}
		if p.ClearAgeRating {
			params.MaxAgeRating = nil
		} else if p.MaxAgeRating != nil {
			params.MaxAgeRating = p.MaxAgeRating
		}
		if err := q.UpdateUser(ctx, params); err != nil {
			return fmt.Errorf("actualizar usuario: %w", err)
		}

		if p.Password != nil {
			err := q.UpdatePassword(ctx, sqlc.UpdatePasswordParams{ID: id, PasswordHash: hash, UpdatedAt: now})
			if err != nil {
				return fmt.Errorf("guardar contraseña: %w", err)
			}
			if err := devices.RevokeAllTx(ctx, q, id, "", now); err != nil {
				return err
			}
		}
		user, err = load(ctx, q, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if p.Password != nil || (p.IsActive != nil && !*p.IsActive) {
		if err := s.closeSessions(ctx, id); err != nil {
			return nil, err
		}
	}
	return user, nil
}

func (s *Service) closeSessions(ctx context.Context, id string) error {
	if s.Sessions == nil {
		return nil
	}
	if err := s.Sessions.TerminateUser(ctx, id); err != nil {
		return fmt.Errorf("cerrar sesiones: %w", err)
	}
	return nil
}

// Delete elimina un usuario de rango inferior, lo que también impide borrarse a
// uno mismo. Sus sesiones de reproducción se cierran antes; dispositivos y
// tokens caen en cascada.
func (s *Service) Delete(ctx context.Context, actor authorization.Actor, id string) error {
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return err
	}
	row, err := load(ctx, s.db.R, id)
	if err != nil {
		return err
	}
	if !authorization.CanManage(actor, media.Role(row.Role)) {
		return core.ErrForbidden()
	}
	if err := s.closeSessions(ctx, id); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(q *sqlc.Queries) error {
		if _, err := load(ctx, q, id); err != nil {
			return err
		}
		if err := q.DeleteUser(ctx, id); err != nil {
			return fmt.Errorf("eliminar usuario: %w", err)
		}
		return nil
	})
}

// Preferences devuelve las preferencias del usuario.
func (s *Service) Preferences(ctx context.Context, userID string) (*Preferences, error) {
	row, err := s.db.R.GetPreferences(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("leer preferencias: %w", err)
	}
	return &Preferences{
		AudioLanguage:    row.AudioLanguage,
		SubtitleLanguage: row.SubtitleLanguage,
		SubtitleMode:     media.SubtitleMode(row.SubtitleMode),
		MaxBitrateRemote: row.MaxBitrateRemote,
	}, nil
}

// SetPreferences sustituye las preferencias del usuario.
func (s *Service) SetPreferences(ctx context.Context, userID string, p Preferences) error {
	if !p.SubtitleMode.Valid() {
		return core.ErrValidation("El modo de subtítulos no es válido")
	}
	for _, lang := range []*string{p.AudioLanguage, p.SubtitleLanguage} {
		if lang != nil && (strings.TrimSpace(*lang) == "" || utf8.RuneCountInString(*lang) > maxLanguageLength) {
			return core.ErrValidation("El idioma no es válido")
		}
	}
	if p.MaxBitrateRemote != nil && *p.MaxBitrateRemote <= 0 {
		return core.ErrValidation("El límite de bitrate debe ser positivo")
	}
	err := s.db.Q.SetPreferences(ctx, sqlc.SetPreferencesParams{
		UserID:           userID,
		AudioLanguage:    p.AudioLanguage,
		SubtitleLanguage: p.SubtitleLanguage,
		SubtitleMode:     string(p.SubtitleMode),
		MaxBitrateRemote: p.MaxBitrateRemote,
	})
	if err != nil {
		return fmt.Errorf("guardar preferencias: %w", err)
	}
	return nil
}

// checkAssignable impide crear un segundo Owner o conceder un rango igual o
// superior al propio.
func checkAssignable(actor authorization.Actor, role media.Role) error {
	if !role.Valid() {
		return core.ErrValidation("El rol no es válido")
	}
	if !authorization.CanManage(actor, role) {
		return core.ErrForbidden()
	}
	return nil
}

func validateAgeRating(rating *int64) error {
	if rating != nil && *rating < 0 {
		return core.ErrValidation("La edad máxima no puede ser negativa")
	}
	return nil
}

func checkUsernameFree(ctx context.Context, q *sqlc.Queries, username, ownID string) error {
	row, err := q.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("buscar usuario: %w", err)
	}
	if row.ID != ownID {
		return core.ErrUsernameTaken()
	}
	return nil
}

func load(ctx context.Context, q *sqlc.Queries, id string) (*sqlc.User, error) {
	row, err := q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer usuario: %w", err)
	}
	return &row, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
