package sessions

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/events"
	"medianode/internal/media"
)

// Tamaños de página.
const (
	DefaultPage = 20
	MaxPage     = 100
)

// Progress atiende el progreso por usuario, «Continuar viendo» y el historial
// (§12.3).
type Progress struct {
	db        *database.DB
	authz     *authorization.Authorizer
	bus       *events.Bus
	threshold float64
	now       func() time.Time
}

// NewProgress crea el servicio. threshold es la fracción que marca un
// elemento como completado.
func NewProgress(db *database.DB, authz *authorization.Authorizer, bus *events.Bus, threshold float64) *Progress {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.9
	}
	return &Progress{db: db, authz: authz, bus: bus, threshold: threshold, now: time.Now}
}

// SetParams es un progreso que envía el cliente, por ejemplo al sincronizar
// tras reproducir sin conexión o al marcar algo como visto.
type SetParams struct {
	ItemID     string
	PositionMS int64
	DurationMS *int64     // Sin él se usa la del elemento.
	Completed  *bool      // Sin él se calcula con el umbral.
	ClientTime *time.Time // Hora del dato; se ignora si es futura.
}

// Set guarda el progreso del actor. Un dato más antiguo que el guardado no lo
// sustituye; en ese caso se devuelve el vigente.
func (p *Progress) Set(ctx context.Context, actor authorization.Actor, sp SetParams) (*sqlc.PlaybackProgress, error) {
	if sp.PositionMS < 0 || (sp.DurationMS != nil && *sp.DurationMS < 0) {
		return nil, core.ErrValidation("La posición o la duración no son válidas")
	}
	if err := p.authz.CanItem(ctx, actor, sp.ItemID); err != nil {
		return nil, err
	}
	item, err := p.db.R.GetMediaItem(ctx, sp.ItemID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer elemento: %w", err)
	}
	duration := sp.DurationMS
	if duration == nil {
		duration = item.DurationMs
	}
	completed := duration != nil && *duration > 0 && float64(sp.PositionMS) >= p.threshold*float64(*duration)
	if sp.Completed != nil {
		completed = *sp.Completed
	}
	at := p.now()
	if sp.ClientTime != nil && !sp.ClientTime.After(at) {
		at = *sp.ClientTime
	}
	_, err = p.db.Q.UpsertProgress(ctx, sqlc.UpsertProgressParams{
		UserID: actor.UserID, MediaItemID: sp.ItemID, PositionMs: sp.PositionMS, DurationMs: duration,
		Completed: boolInt(completed), UpdatedAt: database.FormatTime(at),
	})
	if err != nil {
		return nil, fmt.Errorf("guardar progreso: %w", err)
	}
	cur, err := p.db.Q.GetProgress(ctx, sqlc.GetProgressParams{UserID: actor.UserID, MediaItemID: sp.ItemID})
	if err != nil {
		return nil, fmt.Errorf("leer progreso: %w", err)
	}
	p.bus.Publish(events.Event{Name: events.PlaybackProgress, UserID: actor.UserID, Data: map[string]any{
		"media_item_id": cur.MediaItemID, "position_ms": cur.PositionMs, "completed": cur.Completed != 0,
	}})
	return &cur, nil
}

// ContinueWatching devuelve lo empezado y sin terminar, lo más reciente
// primero, sin lo que el actor ya no puede ver.
func (p *Progress) ContinueWatching(ctx context.Context, actor authorization.Actor, limit int) ([]sqlc.ListContinueWatchingRow, error) {
	params := sqlc.ListContinueWatchingParams{UserID: actor.UserID, PageSize: int64(pageSize(limit))}
	if authorization.IsAdmin(actor) {
		params.Admin = 1
	}
	maxAge := int64(0)
	if actor.Role == media.RoleRestricted {
		params.Restricted = 1
		if actor.MaxAgeRating != nil {
			maxAge = *actor.MaxAgeRating
		}
	}
	params.MaxAge = &maxAge
	rows, err := p.db.R.ListContinueWatching(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("leer continuar viendo: %w", err)
	}
	return rows, nil
}

type historyCursor struct {
	Time string `json:"t"`
	ID   string `json:"i"`
}

// History devuelve el historial del actor, del más reciente al más antiguo.
// Conserva el título aunque el elemento ya no exista.
func (p *Progress) History(ctx context.Context, actor authorization.Actor, after string, limit int) ([]sqlc.ListHistoryRow, string, error) {
	cur := historyCursor{Time: "9999", ID: ""}
	if after != "" {
		data, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || json.Unmarshal(data, &cur) != nil || cur.ID == "" {
			return nil, "", core.ErrValidation("El cursor no es válido")
		}
	}
	size := pageSize(limit)
	rows, err := p.db.R.ListHistory(ctx, sqlc.ListHistoryParams{
		UserID: actor.UserID, BeforeTime: cur.Time, BeforeID: cur.ID, PageSize: int64(size + 1),
	})
	if err != nil {
		return nil, "", fmt.Errorf("leer historial: %w", err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		last := rows[size-1]
		data, _ := json.Marshal(historyCursor{Time: last.WatchedAt, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return rows, next, nil
}

func pageSize(n int) int {
	if n <= 0 {
		return DefaultPage
	}
	return min(n, MaxPage)
}
