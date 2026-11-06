package metadata

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

// peopleNamespace genera identificadores estables de persona a partir del
// proveedor: la misma persona en dos películas es una sola fila.
var peopleNamespace = uuid.MustParse("6f1c1a52-3d8e-4c4f-9a39-7d0f4b1f2e10")

// apply guarda los metadatos de un elemento principal y, para una serie, los
// de sus temporadas y episodios locales. Un elemento bloqueado no se toca.
// Devuelve false si no aplicó nada.
func apply(ctx context.Context, q *sqlc.Queries, provider, lang, itemID string, d *Details, seasons []*Season) (bool, error) {
	now := database.Now()
	sort := media.SortTitle(d.Title, lang)
	n, err := q.ApplyItemMetadata(ctx, sqlc.ApplyItemMetadataParams{
		ID: itemID, Title: d.Title, OriginalTitle: nonEmpty(d.OriginalTitle), SortTitle: &sort,
		Year: d.Year, ReleaseDate: d.ReleaseDate, Overview: nonEmpty(d.Overview),
		AgeRating: d.AgeRating, MinAge: d.MinAge, OriginalLanguage: nonEmpty(d.OriginalLanguage), UpdatedAt: now,
	})
	if err != nil {
		return false, fmt.Errorf("guardar metadatos: %w", err)
	}
	if n == 0 {
		return false, nil
	}

	if err := q.SetExternalID(ctx, sqlc.SetExternalIDParams{MediaItemID: itemID, Provider: provider, ExternalID: d.ExternalID}); err != nil {
		return false, fmt.Errorf("guardar identificador: %w", err)
	}
	for p, id := range d.ExternalIDs {
		if err := q.SetExternalID(ctx, sqlc.SetExternalIDParams{MediaItemID: itemID, Provider: p, ExternalID: id}); err != nil {
			return false, fmt.Errorf("guardar identificador: %w", err)
		}
	}
	if err := setGenres(ctx, q, itemID, d.Genres); err != nil {
		return false, err
	}
	if err := setPeople(ctx, q, provider, itemID, d.People); err != nil {
		return false, err
	}
	for _, t := range []media.ImageType{media.ImagePoster, media.ImageBackdrop, media.ImageLogo} {
		if err := setImage(ctx, q, itemID, t, d.Images[t]); err != nil {
			return false, err
		}
	}
	if len(seasons) > 0 {
		if err := applySeasons(ctx, q, lang, itemID, seasons, now); err != nil {
			return false, err
		}
	}
	return true, nil
}

// applySeasons rellena las temporadas y episodios que existen en disco. Los
// que el proveedor no conoce conservan el título local.
func applySeasons(ctx context.Context, q *sqlc.Queries, lang, seriesID string, seasons []*Season, now string) error {
	byNumber := map[int64]*Season{}
	for _, s := range seasons {
		byNumber[s.Number] = s
	}
	local, err := q.ListChildNumbers(ctx, &seriesID)
	if err != nil {
		return fmt.Errorf("leer temporadas: %w", err)
	}
	for _, ls := range local {
		if ls.SeasonNumber == nil {
			continue
		}
		s := byNumber[*ls.SeasonNumber]
		if s == nil {
			continue
		}
		if err := applyChild(ctx, q, lang, ls.ID, s.Title, s.Overview, s.AirDate, now); err != nil {
			return err
		}
		if err := setImage(ctx, q, ls.ID, media.ImagePoster, s.PosterURL); err != nil {
			return err
		}
		episodes := map[int64]Episode{}
		for _, e := range s.Episodes {
			episodes[e.Number] = e
		}
		eps, err := q.ListChildNumbers(ctx, &ls.ID)
		if err != nil {
			return fmt.Errorf("leer episodios: %w", err)
		}
		for _, le := range eps {
			if le.EpisodeNumber == nil {
				continue
			}
			e, ok := episodes[*le.EpisodeNumber]
			if !ok && len(local) == 1 && *ls.SeasonNumber == 1 {
				e, ok = absoluteEpisode(seasons, *le.EpisodeNumber)
			}
			if !ok {
				continue
			}
			if err := applyChild(ctx, q, lang, le.ID, e.Title, e.Overview, e.AirDate, now); err != nil {
				return err
			}
			if err := setImage(ctx, q, le.ID, media.ImageThumbnail, e.StillURL); err != nil {
				return err
			}
		}
	}
	return nil
}

// absoluteEpisode busca el episodio n contando de corrido las temporadas del
// proveedor en orden, sin los especiales (temporada 0). Sirve a las series
// que en disco son una sola temporada numerada 1..N.
func absoluteEpisode(seasons []*Season, n int64) (Episode, bool) {
	ordered := slices.Clone(seasons)
	slices.SortFunc(ordered, func(a, b *Season) int { return cmp.Compare(a.Number, b.Number) })
	var offset int64
	for _, s := range ordered {
		if s.Number < 1 {
			continue
		}
		var top int64
		for _, e := range s.Episodes {
			top = max(top, e.Number)
		}
		if n <= offset+top {
			for _, e := range s.Episodes {
				if e.Number == n-offset {
					return e, true
				}
			}
			return Episode{}, false
		}
		offset += top
	}
	return Episode{}, false
}

// applyChild guarda una temporada o un episodio. Su clasificación queda vacía
// para que herede la de la serie (§15.1).
func applyChild(ctx context.Context, q *sqlc.Queries, lang, id, title, overview string, airDate *string, now string) error {
	if strings.TrimSpace(title) == "" {
		return nil
	}
	sort := media.SortTitle(title, lang)
	var year *int64
	if airDate != nil && len(*airDate) >= 4 {
		var y int64
		if _, err := fmt.Sscanf((*airDate)[:4], "%d", &y); err == nil {
			year = &y
		}
	}
	_, err := q.ApplyItemMetadata(ctx, sqlc.ApplyItemMetadataParams{
		ID: id, Title: title, SortTitle: &sort, Year: year, ReleaseDate: airDate,
		Overview: nonEmpty(overview), UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("guardar episodio: %w", err)
	}
	return nil
}

func setGenres(ctx context.Context, q *sqlc.Queries, itemID string, genres []string) error {
	if err := q.DeleteItemGenres(ctx, itemID); err != nil {
		return fmt.Errorf("borrar géneros: %w", err)
	}
	for _, name := range genres {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		id, err := q.GetGenreByName(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			if id, err = database.NewID(); err == nil {
				err = q.CreateGenre(ctx, sqlc.CreateGenreParams{ID: id, Name: name})
			}
		}
		if err != nil {
			return fmt.Errorf("guardar género: %w", err)
		}
		if err := q.AddItemGenre(ctx, sqlc.AddItemGenreParams{MediaItemID: itemID, GenreID: id}); err != nil {
			return fmt.Errorf("guardar género: %w", err)
		}
	}
	return nil
}

func setPeople(ctx context.Context, q *sqlc.Queries, provider, itemID string, people []Person) error {
	if err := q.DeleteItemPeople(ctx, itemID); err != nil {
		return fmt.Errorf("borrar personas: %w", err)
	}
	for _, p := range people {
		if p.ExternalID == "" || strings.TrimSpace(p.Name) == "" {
			continue
		}
		id := uuid.NewSHA1(peopleNamespace, []byte(provider+"/"+p.ExternalID)).String()
		if err := q.UpsertPerson(ctx, sqlc.UpsertPersonParams{ID: id, Name: p.Name}); err != nil {
			return fmt.Errorf("guardar persona: %w", err)
		}
		err := q.AddItemPerson(ctx, sqlc.AddItemPersonParams{
			MediaItemID: itemID, PersonID: id, Role: p.Role, CharacterName: nonEmpty(p.Character), SortOrder: p.Order,
		})
		if err != nil {
			return fmt.Errorf("guardar persona: %w", err)
		}
	}
	return nil
}

// setImage registra la URL de origen; la descarga llega después, fuera de la
// transacción. Sin URL, la imagen se olvida.
func setImage(ctx context.Context, q *sqlc.Queries, itemID string, t media.ImageType, url string) error {
	if url == "" {
		if err := q.DeleteItemImage(ctx, sqlc.DeleteItemImageParams{MediaItemID: itemID, Type: string(t)}); err != nil {
			return fmt.Errorf("borrar imagen: %w", err)
		}
		return nil
	}
	id, err := database.NewID()
	if err != nil {
		return err
	}
	err = q.UpsertImage(ctx, sqlc.UpsertImageParams{ID: id, MediaItemID: itemID, Type: string(t), SourceUrl: &url})
	if err != nil {
		return fmt.Errorf("guardar imagen: %w", err)
	}
	return nil
}

// choose elige la coincidencia segura entre los primeros resultados, o nil. Con
// año local, basta un año a ±1 en el primer resultado o con el mismo título;
// sin año, hace falta el mismo título. Un título igual con otro año es otra
// película (una nueva versión) y no se acepta.
func choose(q Query, matches []Match) *Match {
	want := normalize(q.Title)
	for i, m := range matches[:min(len(matches), 10)] {
		sameTitle := normalize(m.Title) == want || normalize(m.OriginalTitle) == want
		switch {
		case q.Year != nil:
			if m.Year != nil && abs(*m.Year-*q.Year) <= 1 && (sameTitle || i == 0) {
				return &matches[i]
			}
		case sameTitle:
			return &matches[i]
		}
	}
	return nil
}

// normalize compara títulos sin acentos, mayúsculas ni signos.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range media.SortTitle(s, "xx") {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func nonEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
