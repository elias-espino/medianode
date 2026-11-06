package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
)

// mergeSameSeries une dos series de la misma biblioteca que el proveedor
// identifica como una sola: pasa cuando sus carpetas se llaman distinto y el
// escáner, que agrupa por nombre, las creó por separado. Se queda la más
// antigua y recibe las temporadas y episodios de la otra; la serie resultante
// vuelve a la cola para pedir los datos de todas sus temporadas. Devuelve true
// si unió algo (el elemento itemID puede haber dejado de existir).
func mergeSameSeries(ctx context.Context, q *sqlc.Queries, provider, externalID, itemID string) (bool, error) {
	item, err := q.GetMediaItem(ctx, itemID)
	if err != nil {
		return false, fmt.Errorf("leer serie: %w", err)
	}
	other, err := q.FindSameExternalItem(ctx, sqlc.FindSameExternalItemParams{
		Provider: provider, ExternalID: externalID, LibraryID: item.LibraryID, Type: item.Type, ID: itemID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("buscar duplicada: %w", err)
	}

	keeper, source := other.ID, itemID
	newerKey := item.MatchKey
	if item.CreatedAt < other.CreatedAt || (item.CreatedAt == other.CreatedAt && item.ID < other.ID) {
		keeper, source = itemID, other.ID
		newerKey = other.MatchKey
	}
	now := database.Now()

	seasons, err := q.ListChildNumbers(ctx, &source)
	if err != nil {
		return false, fmt.Errorf("leer temporadas: %w", err)
	}
	for _, s := range seasons {
		if s.SeasonNumber == nil {
			continue
		}
		target, err := q.GetSeason(ctx, sqlc.GetSeasonParams{ParentID: &keeper, SeasonNumber: s.SeasonNumber})
		if errors.Is(err, sql.ErrNoRows) {
			if err := q.ReparentItem(ctx, sqlc.ReparentItemParams{ID: s.ID, ParentID: &keeper, UpdatedAt: now}); err != nil {
				return false, fmt.Errorf("mover temporada: %w", err)
			}
			continue
		}
		if err != nil {
			return false, err
		}
		episodes, err := q.ListChildNumbers(ctx, &s.ID)
		if err != nil {
			return false, fmt.Errorf("leer episodios: %w", err)
		}
		for _, e := range episodes {
			if e.EpisodeNumber == nil {
				continue
			}
			dest, err := q.GetEpisode(ctx, sqlc.GetEpisodeParams{ParentID: &target, EpisodeNumber: e.EpisodeNumber})
			if errors.Is(err, sql.ErrNoRows) {
				err = q.ReparentItem(ctx, sqlc.ReparentItemParams{ID: e.ID, ParentID: &target, UpdatedAt: now})
			} else if err == nil {
				err = q.MoveFileLinks(ctx, sqlc.MoveFileLinksParams{MediaItemID: e.ID, MediaItemID_2: dest})
			}
			if err != nil {
				return false, fmt.Errorf("mover episodio: %w", err)
			}
		}
	}
	// Lo que quede de la otra (y sus imágenes, géneros y demás) se va con ella.
	if err := q.DeleteItemByID(ctx, source); err != nil {
		return false, fmt.Errorf("borrar duplicada: %w", err)
	}
	// La clave de la más reciente refleja cómo se llaman ahora las carpetas: los
	// archivos nuevos con ese nombre caen en esta serie y no crean otra.
	if newerKey != nil {
		if err := q.SetMatchKey(ctx, sqlc.SetMatchKeyParams{ID: keeper, MatchKey: newerKey}); err != nil {
			return false, fmt.Errorf("guardar clave: %w", err)
		}
	}
	if err := q.RequeueSeriesMetadata(ctx, sqlc.RequeueSeriesMetadataParams{ID: keeper, UpdatedAt: now}); err != nil {
		return false, err
	}
	return true, nil
}

// mergeDuplicates une las series que ya comparten identificador: las creadas
// antes de que el enriquecedor supiera unirlas, o las que el administrador
// identificó a mano con el mismo.
func (e *Enricher) mergeDuplicates(ctx context.Context) error {
	rows, err := e.db.R.ListDuplicateSeries(ctx, e.provider.Name())
	if err != nil {
		return fmt.Errorf("buscar series duplicadas: %w", err)
	}
	merged := false
	for _, r := range rows {
		err := e.db.InTx(ctx, func(q *sqlc.Queries) error {
			ok, err := mergeSameSeries(ctx, q, e.provider.Name(), r.ExternalID, r.ID)
			merged = merged || ok
			return err
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue // Ya se unió con otra de su grupo.
		}
		if err != nil {
			return err
		}
	}
	if merged {
		e.Notify()
	}
	return nil
}
