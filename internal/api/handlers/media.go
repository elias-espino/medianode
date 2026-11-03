package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/api"
	"medianode/internal/api/dto"
	"medianode/internal/core"
	"medianode/internal/database/sqlc"
	"medianode/internal/images"
	"medianode/internal/media"
	"medianode/internal/metadata"
	"medianode/internal/readmodel"
)

// imageMaxAge es lo que el navegador reutiliza una imagen sin preguntar. Con
// ETag, pasado ese tiempo solo revalida.
const imageMaxAge = "86400"

// Media atiende /media, /libraries/{id}/items y /search.
type Media struct {
	Catalog  *readmodel.Catalog
	Metadata *metadata.Service
	Images   *images.Manager
	Log      zerolog.Logger
}

// ListItems devuelve una página de la biblioteca.
func (h *Media) ListItems(w http.ResponseWriter, r *http.Request) {
	size, err := intParam(r, "limit")
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	q := r.URL.Query()
	page, err := h.Catalog.ListLibraryItems(r.Context(), mustActor(r), chi.URLParam(r, "id"), readmodel.ListParams{
		After: q.Get("cursor"), Size: size, Status: media.MetadataStatus(q.Get("metadata_status")),
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := dto.ItemPage{Items: make([]dto.ItemSummary, len(page.Items))}
	for i, it := range page.Items {
		out.Items[i] = dto.ItemSummary{
			ID: it.ID, Type: it.Type, Title: it.Title, Year: it.Year, MetadataStatus: it.MetadataStatus,
		}
		if it.HasPoster {
			out.Items[i].PosterURL = imageURL(it.ID, media.ImagePoster)
		}
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Get devuelve el detalle de un elemento.
func (h *Media) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.Catalog.Item(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, itemView(item))
}

// Children devuelve las temporadas o los episodios.
func (h *Media) Children(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Catalog.Children(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.Child, len(rows))
	for i, c := range rows {
		out[i] = dto.Child{
			ID: c.ID, Type: c.Type, Title: c.Title, Year: c.Year, SeasonNumber: c.SeasonNumber,
			EpisodeNumber: c.EpisodeNumber, Overview: c.Overview, ReleaseDate: c.ReleaseDate, DurationMS: c.DurationMs,
		}
		if c.HasImage != 0 {
			t := media.ImagePoster
			if c.Type == string(media.ItemEpisode) {
				t = media.ImageThumbnail
			}
			out[i].ImageURL = imageURL(c.ID, t)
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Files devuelve las versiones de un elemento.
func (h *Media) Files(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Catalog.Files(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.MediaFile, len(rows))
	for i, f := range rows {
		out[i] = dto.MediaFile{
			ID: f.ID, RelativePath: optional(f.RelativePath), Container: f.Container, FileSize: f.FileSize,
			DurationMS: f.DurationMs, Bitrate: f.Bitrate, ProbeStatus: f.ProbeStatus,
			MissingSince: f.MissingSince, PartOrder: f.PartOrder,
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Streams devuelve las pistas.
func (h *Media) Streams(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Catalog.Streams(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.Stream, len(rows))
	for i, s := range rows {
		out[i] = streamView(&s)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Subtitles devuelve los subtítulos externos.
func (h *Media) Subtitles(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Catalog.Subtitles(r.Context(), mustActor(r), chi.URLParam(r, "id"))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.Subtitle, len(rows))
	for i, s := range rows {
		out[i] = dto.Subtitle{
			ID: s.ID, FileID: s.MediaFileID, RelativePath: optional(s.RelativePath), Language: s.Language,
			Format: s.Format, Default: s.IsDefault != 0, Forced: s.IsForced != 0,
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Image sirve una imagen descargada. Es privada: solo la ve quien puede ver el
// elemento, así que ningún proxy compartido debe guardarla.
func (h *Media) Image(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rel, err := h.Catalog.Image(r.Context(), mustActor(r), id, media.ImageType(chi.URLParam(r, "type")))
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	f, err := h.Images.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		api.WriteError(w, r, core.ErrNotFound())
		return
	}
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, max-age="+imageMaxAge)
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, f.Info.Size(), f.Info.ModTime().UnixNano()))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent responde 304 a If-None-Match y deduce el tipo por la extensión.
	http.ServeContent(w, r, f.Info.Name(), f.Info.ModTime(), f)
}

// Search busca en las bibliotecas visibles.
func (h *Media) Search(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit")
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	rows, err := h.Catalog.Search(r.Context(), mustActor(r), r.URL.Query().Get("q"), limit)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	out := make([]dto.SearchResult, len(rows))
	for i, s := range rows {
		out[i] = dto.SearchResult{
			ID: s.ID, Type: s.Type, Title: s.Title, Year: s.Year, LibraryID: s.LibraryID,
			ParentID: s.ParentID, ParentTitle: s.ParentTitle,
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Matches busca coincidencias en el proveedor.
func (h *Media) Matches(w http.ResponseWriter, r *http.Request) {
	var year *int64
	if v := r.URL.Query().Get("year"); v != "" {
		y, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			api.WriteError(w, r, core.ErrValidation("El año no es válido"))
			return
		}
		year = &y
	}
	rows, err := h.Metadata.Matches(r.Context(), mustActor(r), chi.URLParam(r, "id"), r.URL.Query().Get("query"), year)
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	provider := h.Metadata.ProviderName()
	out := make([]dto.Match, len(rows))
	for i, m := range rows {
		out[i] = dto.Match{
			Provider: provider, ExternalID: m.ExternalID, Title: m.Title, OriginalTitle: m.OriginalTitle,
			Year: m.Year, Overview: m.Overview, PosterURL: optional(m.PosterURL),
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Identify aplica la coincidencia elegida y devuelve el elemento actualizado.
func (h *Media) Identify(w http.ResponseWriter, r *http.Request) {
	var req dto.IdentifyRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	actor, id := mustActor(r), chi.URLParam(r, "id")
	if err := h.Metadata.Identify(r.Context(), actor, id, req.Provider, req.ExternalID); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.Get(w, r)
}

// Refresh devuelve el elemento a la cola de metadatos.
func (h *Media) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := h.Metadata.Refresh(r.Context(), mustActor(r), chi.URLParam(r, "id")); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// EditMetadata cambia y bloquea los metadatos.
func (h *Media) EditMetadata(w http.ResponseWriter, r *http.Request) {
	var req dto.EditMetadataRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	err := h.Metadata.Edit(r.Context(), mustActor(r), chi.URLParam(r, "id"), metadata.EditParams{
		Title:         req.Title,
		OriginalTitle: field(req.OriginalTitle),
		Overview:      field(req.Overview),
		Year:          field(req.Year),
		ReleaseDate:   field(req.ReleaseDate),
		AgeRating:     field(req.AgeRating),
		MinAge:        field(req.MinAge),
		Locked:        req.Locked,
	})
	if err != nil {
		api.WriteFailure(w, r, h.Log, err)
		return
	}
	h.Get(w, r)
}

func field[T any](o dto.Optional[T]) metadata.Field[T] {
	return metadata.Field[T]{Set: o.Set, Value: o.Value}
}

func itemView(it *readmodel.ItemDetail) dto.Item {
	out := dto.Item{
		ID: it.ID, LibraryID: it.LibraryID, ParentID: it.ParentID, Type: it.Type, Title: it.Title,
		OriginalTitle: it.OriginalTitle, Year: it.Year, ReleaseDate: it.ReleaseDate, Overview: it.Overview,
		AgeRating: it.AgeRating, MinAge: it.MinAge, OriginalLanguage: it.OriginalLanguage,
		DurationMS: it.DurationMs, SeasonNumber: it.SeasonNumber, EpisodeNumber: it.EpisodeNumber,
		MetadataStatus: it.MetadataStatus, MetadataLocked: it.MetadataLocked != 0,
		Genres: it.Genres, People: []dto.Person{}, ExternalIDs: map[string]string{}, Images: map[string]string{},
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
	}
	for _, p := range it.People {
		out.People = append(out.People, dto.Person{ID: p.ID, Name: p.Name, Role: p.Role, Character: p.CharacterName})
	}
	for _, e := range it.ExternalIDs {
		out.ExternalIDs[e.Provider] = e.ExternalID
	}
	for _, t := range it.Images {
		out.Images[string(t)] = *imageURL(it.ID, t)
	}
	return out
}

func streamView(s *sqlc.MediaStream) dto.Stream {
	out := dto.Stream{
		ID: s.ID, FileID: s.MediaFileID, Index: s.StreamIndex, Type: s.StreamType, Codec: s.Codec,
		Profile: s.Profile, Level: s.Level, Language: s.Language, Channels: s.Channels, Width: s.Width,
		Height: s.Height, FrameRate: s.FrameRate, Bitrate: s.Bitrate, BitDepth: s.BitDepth,
		ColorTransfer: s.ColorTransfer, ColorPrimaries: s.ColorPrimaries, HDRFormat: s.HdrFormat,
		Default: s.IsDefault != 0, Forced: s.IsForced != 0,
	}
	if s.StreamMetadataJson != nil {
		var meta struct {
			Title *string `json:"title"`
		}
		if json.Unmarshal([]byte(*s.StreamMetadataJson), &meta) == nil {
			out.Title = meta.Title
		}
	}
	return out
}

// imageURL es la ruta de la API para una imagen; el cliente la pide con su
// propia credencial.
func imageURL(id string, t media.ImageType) *string {
	u := "/api/v1/media/" + id + "/images/" + string(t)
	return &u
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// intParam lee un entero opcional de la consulta; 0 si no está.
func intParam(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, core.ErrValidation("El parámetro " + name + " no es válido")
	}
	return n, nil
}
