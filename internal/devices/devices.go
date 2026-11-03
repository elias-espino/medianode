// Package devices gestiona los clientes vinculados.
package devices

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
	"medianode/internal/events"
	"medianode/internal/media"
)

const maxNameLength = 100

// SessionCloser cierra las sesiones de reproducción de un dispositivo.
type SessionCloser interface {
	TerminateDevice(ctx context.Context, deviceID string) error
}

// Service gestiona los dispositivos.
type Service struct {
	db *database.DB

	// Sessions, si no es nil, cierra las sesiones del dispositivo revocado.
	Sessions SessionCloser
	// Bus, si no es nil, recibe device.revoked para el dueño del dispositivo.
	Bus *events.Bus
}

// New crea el servicio.
func New(db *database.DB) *Service {
	return &Service{db: db}
}

// RevokeTx revoca el dispositivo y sus tokens dentro de una transacción. El
// middleware consulta revoked_at en cada petición, por lo que el corte es
// inmediato aunque el access token siga vigente.
func RevokeTx(ctx context.Context, q *sqlc.Queries, deviceID, now string) error {
	if err := q.RevokeDevice(ctx, sqlc.RevokeDeviceParams{ID: deviceID, RevokedAt: &now}); err != nil {
		return fmt.Errorf("revocar dispositivo: %w", err)
	}
	err := q.RevokeDeviceTokens(ctx, sqlc.RevokeDeviceTokensParams{DeviceID: deviceID, RevokedAt: &now})
	if err != nil {
		return fmt.Errorf("revocar tokens: %w", err)
	}
	return nil
}

// RevokeAllTx revoca los dispositivos activos del usuario salvo keepDeviceID.
func RevokeAllTx(ctx context.Context, q *sqlc.Queries, userID, keepDeviceID, now string) error {
	ids, err := q.ListActiveUserDeviceIDs(ctx, userID)
	if err != nil {
		return fmt.Errorf("listar dispositivos: %w", err)
	}
	for _, id := range ids {
		if id == keepDeviceID {
			continue
		}
		if err := RevokeTx(ctx, q, id, now); err != nil {
			return err
		}
	}
	return nil
}

// List devuelve los dispositivos propios o, con all, los de todos los usuarios.
func (s *Service) List(ctx context.Context, actor authorization.Actor, all bool) ([]sqlc.Device, error) {
	if !all {
		rows, err := s.db.R.ListUserDevices(ctx, actor.UserID)
		if err != nil {
			return nil, fmt.Errorf("listar dispositivos: %w", err)
		}
		return rows, nil
	}
	if err := authorization.RequireRole(actor, media.RoleAdmin); err != nil {
		return nil, err
	}
	rows, err := s.db.R.ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("listar dispositivos: %w", err)
	}
	return rows, nil
}

// Get devuelve un dispositivo que el actor puede ver.
func (s *Service) Get(ctx context.Context, actor authorization.Actor, id string) (*sqlc.Device, error) {
	return s.load(ctx, s.db.R, actor, id)
}

// Rename cambia el nombre visible.
func (s *Service) Rename(ctx context.Context, actor authorization.Actor, id, name string) (*sqlc.Device, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLength {
		return nil, core.ErrValidation("El nombre del dispositivo no es válido")
	}
	var out *sqlc.Device
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		dev, err := s.load(ctx, q, actor, id)
		if err != nil {
			return err
		}
		if err := q.RenameDevice(ctx, sqlc.RenameDeviceParams{ID: id, DeviceName: name}); err != nil {
			return fmt.Errorf("renombrar dispositivo: %w", err)
		}
		dev.DeviceName = name
		out = dev
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Revoke retira el acceso del dispositivo.
func (s *Service) Revoke(ctx context.Context, actor authorization.Actor, id string) error {
	var owner string
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		dev, err := s.load(ctx, q, actor, id)
		if err != nil {
			return err
		}
		owner = dev.UserID
		return RevokeTx(ctx, q, id, database.Now())
	})
	if err != nil {
		return err
	}
	s.Bus.Publish(events.Event{Name: events.DeviceRevoked, UserID: owner, Data: map[string]any{"device_id": id}})
	if s.Sessions != nil {
		if err := s.Sessions.TerminateDevice(ctx, id); err != nil {
			return fmt.Errorf("cerrar sesiones: %w", err)
		}
	}
	return nil
}

// load devuelve 404 también para un dispositivo ajeno: así no se revela que
// existe.
func (s *Service) load(ctx context.Context, q *sqlc.Queries, actor authorization.Actor, id string) (*sqlc.Device, error) {
	dev, err := q.GetDevice(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer dispositivo: %w", err)
	}
	if dev.UserID == actor.UserID {
		return &dev, nil
	}
	owner, err := q.GetUser(ctx, dev.UserID)
	if err != nil {
		return nil, fmt.Errorf("leer propietario del dispositivo: %w", err)
	}
	if !authorization.CanManage(actor, media.Role(owner.Role)) || authorization.RequireRole(actor, media.RoleAdmin) != nil {
		return nil, core.ErrNotFound()
	}
	return &dev, nil
}
