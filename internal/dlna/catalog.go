package dlna

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/playback"
)

const (
	rootID       = "0"
	rootParent   = "-1"
	libPrefix    = "lib:"
	itemPrefix   = "item:"
	maxPage      = 500 // RequestedCount=0 significa «todo»; se acota.
	maxTitleRune = 200
)

// reDBID reconoce los identificadores que genera el servidor. Todo lo que llega
// de la red se valida antes de consultar.
var reDBID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// browseRequest son los argumentos de Browse ya interpretados.
type browseRequest struct {
	objectID string
	metadata bool // BrowseMetadata; si no, BrowseDirectChildren.
	start    int64
	count    int64
	desc     bool // Orden descendente por título.
	host     string
}

// browseResult es la respuesta de Browse.
type browseResult struct {
	didl     string
	returned int
	total    int64
	updateID uint32
}

// browse atiende ContentDirectory#Browse sobre el árbol: raíz → bibliotecas con
// dlna_enabled → elementos; las series se abren en temporadas y episodios.
func (s *Server) browse(ctx context.Context, r browseRequest) (*browseResult, *upnpError) {
	var objs []object
	var total int64
	var err *upnpError
	switch {
	case r.objectID == rootID:
		objs, total, err = s.browseRoot(ctx, r)
	case strings.HasPrefix(r.objectID, libPrefix):
		objs, total, err = s.browseLibrary(ctx, r, strings.TrimPrefix(r.objectID, libPrefix))
	case strings.HasPrefix(r.objectID, itemPrefix):
		objs, total, err = s.browseItem(ctx, r, strings.TrimPrefix(r.objectID, itemPrefix))
	default:
		err = &upnpError{errNoSuchObject, "No such object"}
	}
	if err != nil {
		return nil, err
	}
	return &browseResult{didl: didl(objs), returned: len(objs), total: total, updateID: s.updateID(ctx)}, nil
}

// window recorta una lista según StartingIndex y RequestedCount.
func window[T any](all []T, start, count int64) []T {
	if start >= int64(len(all)) {
		return nil
	}
	end := int64(len(all))
	if count > 0 && start+count < end {
		end = start + count
	}
	return all[start:end]
}

func pageSize(count int64) int64 {
	if count <= 0 || count > maxPage {
		return maxPage
	}
	return count
}

func (s *Server) browseRoot(ctx context.Context, r browseRequest) ([]object, int64, *upnpError) {
	libs, err := s.db.R.ListDLNALibraries(ctx)
	if err != nil {
		return nil, 0, s.failed(err)
	}
	root := object{id: rootID, parent: rootParent, title: s.opts.Name, container: true, childCount: len(libs), class: classRoot}
	if r.metadata {
		return []object{root}, 1, nil
	}
	var objs []object
	for _, l := range libs {
		n, err := s.db.R.CountDLNALibraryItems(ctx, l.ID)
		if err != nil {
			return nil, 0, s.failed(err)
		}
		objs = append(objs, object{id: libPrefix + l.ID, parent: rootID, title: l.Name, container: true, childCount: int(n), class: classContainer})
	}
	return window(objs, r.start, r.count), int64(len(objs)), nil
}

func (s *Server) browseLibrary(ctx context.Context, r browseRequest, id string) ([]object, int64, *upnpError) {
	if !reDBID.MatchString(id) {
		return nil, 0, &upnpError{errNoSuchObject, "No such object"}
	}
	lib, err := s.db.R.GetDLNALibrary(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, &upnpError{errNoSuchObject, "No such object"}
	}
	if err != nil {
		return nil, 0, s.failed(err)
	}
	total, err := s.db.R.CountDLNALibraryItems(ctx, id)
	if err != nil {
		return nil, 0, s.failed(err)
	}
	if r.metadata {
		return []object{{id: libPrefix + id, parent: rootID, title: lib.Name, container: true, childCount: int(total), class: classContainer}}, 1, nil
	}
	var rows []sqlc.GetDLNAItemRow
	size := pageSize(r.count)
	if r.desc {
		items, err := s.db.R.ListDLNALibraryItemsDesc(ctx, sqlc.ListDLNALibraryItemsDescParams{LibraryID: id, PageSize: size, PageOffset: r.start})
		if err != nil {
			return nil, 0, s.failed(err)
		}
		for _, i := range items {
			rows = append(rows, itemRow(i.ID, i.LibraryID, i.ParentID, i.Type, i.Title, i.Year, i.Overview, i.ReleaseDate, i.DurationMs, i.SeasonNumber, i.EpisodeNumber, i.AgeRating, lib.Type))
		}
	} else {
		items, err := s.db.R.ListDLNALibraryItems(ctx, sqlc.ListDLNALibraryItemsParams{LibraryID: id, PageSize: size, PageOffset: r.start})
		if err != nil {
			return nil, 0, s.failed(err)
		}
		for _, i := range items {
			rows = append(rows, itemRow(i.ID, i.LibraryID, i.ParentID, i.Type, i.Title, i.Year, i.Overview, i.ReleaseDate, i.DurationMs, i.SeasonNumber, i.EpisodeNumber, i.AgeRating, lib.Type))
		}
	}
	objs, uerr := s.objects(ctx, r.host, rows)
	return objs, total, uerr
}

func (s *Server) browseItem(ctx context.Context, r browseRequest, id string) ([]object, int64, *upnpError) {
	if !reDBID.MatchString(id) {
		return nil, 0, &upnpError{errNoSuchObject, "No such object"}
	}
	it, err := s.db.R.GetDLNAItem(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, &upnpError{errNoSuchObject, "No such object"}
	}
	if err != nil {
		return nil, 0, s.failed(err)
	}
	if r.metadata {
		objs, uerr := s.objects(ctx, r.host, []sqlc.GetDLNAItemRow{it})
		return objs, 1, uerr
	}
	if t := media.ItemType(it.Type); t != media.ItemSeries && t != media.ItemSeason {
		return nil, 0, &upnpError{710, "No such container"}
	}
	total, err := s.db.R.CountDLNAChildren(ctx, &id)
	if err != nil {
		return nil, 0, s.failed(err)
	}
	kids, err := s.db.R.ListDLNAChildren(ctx, sqlc.ListDLNAChildrenParams{ParentID: &id, PageSize: pageSize(r.count), PageOffset: r.start})
	if err != nil {
		return nil, 0, s.failed(err)
	}
	var rows []sqlc.GetDLNAItemRow
	for _, k := range kids {
		rows = append(rows, itemRow(k.ID, k.LibraryID, k.ParentID, k.Type, k.Title, k.Year, k.Overview, k.ReleaseDate, k.DurationMs, k.SeasonNumber, k.EpisodeNumber, k.AgeRating, it.LibraryType))
	}
	objs, uerr := s.objects(ctx, r.host, rows)
	return objs, total, uerr
}

func itemRow(id, lib string, parent *string, typ, title string, year *int64, overview, release *string, dur, season, episode *int64, age *string, libType string) sqlc.GetDLNAItemRow {
	return sqlc.GetDLNAItemRow{ID: id, LibraryID: lib, ParentID: parent, Type: typ, Title: title, Year: year, Overview: overview,
		ReleaseDate: release, DurationMs: dur, SeasonNumber: season, EpisodeNumber: episode, AgeRating: age, LibraryType: libType}
}

// objects convierte filas en objetos DLNA.
func (s *Server) objects(ctx context.Context, host string, rows []sqlc.GetDLNAItemRow) ([]object, *upnpError) {
	out := make([]object, 0, len(rows))
	for _, row := range rows {
		o, err := s.object(ctx, host, row)
		if err != nil {
			return nil, s.failed(err)
		}
		if o != nil {
			out = append(out, *o)
		}
	}
	return out, nil
}

func parentOf(row sqlc.GetDLNAItemRow) string {
	if row.ParentID != nil {
		return itemPrefix + *row.ParentID
	}
	return libPrefix + row.LibraryID
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// object construye el contenedor o elemento de una fila. Un elemento sin
// archivo reproducible no se publica (nil).
func (s *Server) object(ctx context.Context, host string, row sqlc.GetDLNAItemRow) (*object, error) {
	o := &object{id: itemPrefix + row.ID, parent: parentOf(row), title: clip(titleOf(row), maxTitleRune), class: classContainer}
	if row.ReleaseDate != nil && len(*row.ReleaseDate) >= 10 {
		o.date = (*row.ReleaseDate)[:10]
	} else if row.Year != nil {
		o.date = fmt.Sprintf("%04d-01-01", *row.Year)
	}
	if row.Overview != nil {
		o.desc = clip(*row.Overview, 1000)
	}
	if path, err := s.db.R.GetItemImage(ctx, sqlc.GetItemImageParams{MediaItemID: row.ID, Type: string(media.ImagePoster)}); err == nil && path != nil {
		o.art = "http://" + host + "/dlna/art/" + row.ID + ".jpg"
	}
	switch media.ItemType(row.Type) {
	case media.ItemSeries, media.ItemSeason:
		n, err := s.db.R.CountDLNAChildren(ctx, &row.ID)
		if err != nil {
			return nil, err
		}
		o.container, o.childCount = true, int(n)
		return o, nil
	case media.ItemMovie:
		o.class = classMovie
	case media.ItemEpisode:
		o.class = classEpisode
	default:
		return nil, nil
	}
	res, err := s.resourceFor(ctx, host, row)
	if err != nil || res == nil {
		return nil, err
	}
	o.res = []resource{*res}
	if res.fileID != "" {
		if sub := s.pickSidecar(ctx, res.fileID); sub != nil {
			u := "http://" + host + "/dlna/sub/" + sub.ID + ".srt"
			o.caption = u
			o.res = append(o.res, resource{url: u, protocolInfo: "http-get:*:text/srt:*"})
		}
	}
	return o, nil
}

func titleOf(row sqlc.GetDLNAItemRow) string {
	if media.ItemType(row.Type) == media.ItemEpisode && row.SeasonNumber != nil && row.EpisodeNumber != nil {
		return fmt.Sprintf("%dx%02d %s", *row.SeasonNumber, *row.EpisodeNumber, row.Title)
	}
	if media.ItemType(row.Type) == media.ItemSeason && row.SeasonNumber != nil && strings.TrimSpace(row.Title) == "" {
		return fmt.Sprintf("Temporada %d", *row.SeasonNumber)
	}
	return row.Title
}

// resourceFor decide el <res> de un elemento: el archivo original o el flujo
// MPEG-TS, según playback.PlanDLNA. Sin archivo reproducible, nil.
func (s *Server) resourceFor(ctx context.Context, host string, row sqlc.GetDLNAItemRow) (*resource, error) {
	files, err := s.db.R.ListItemFiles(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.MissingSince != nil || f.ProbeStatus != string(media.ProbeDone) {
			continue
		}
		plan, err := s.playback.PlanDLNA(ctx, row.ID, f.ID, s.planOptions(true))
		if err != nil {
			continue // Un archivo con problemas no esconde a los demás.
		}
		res := &resource{durationMS: plan.DurationMS}
		if v := plan.Video; v != nil {
			res.width, res.height = deref(v.Width), deref(v.Height)
		}
		if a := plan.Audio; a != nil {
			res.channels = deref(a.Channels)
		}
		if plan.Direct {
			ext := strings.ToLower(plan.Container)
			if ext == "" {
				ext = "mp4"
			}
			res.url = "http://" + host + "/dlna/media/" + f.ID + "/" + fileSlug(row.Title, ext)
			res.protocolInfo = protocolInfo(playback.DLNAMIME(plan.Container), "01", false)
			res.size = deref(f.FileSize)
			res.fileID = f.ID
			res.bitrate = deref(f.Bitrate) / 8
		} else {
			res.url = "http://" + host + "/dlna/ts/" + f.ID + "/" + fileSlug(row.Title, "ts")
			res.protocolInfo = protocolInfo("video/mpeg", "10", true)
		}
		return res, nil
	}
	return nil, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// updateID es el SystemUpdateID: sube cuando cambia el catálogo o las
// bibliotecas. Se deriva de la última modificación para no llevar un contador
// en memoria que se pierda al reiniciar.
func (s *Server) updateID(ctx context.Context) uint32 {
	stamp, err := s.db.R.GetDLNAUpdateStamp(ctx)
	if err != nil || stamp == "" {
		return 1
	}
	if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return uint32(t.Unix())
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(stamp))
	return h.Sum32()
}

func (s *Server) failed(err error) *upnpError {
	s.log.Error().Err(err).Msg("DLNA: no se pudo leer el catálogo")
	return &upnpError{errActionFailed, "Action Failed"}
}

// parseSort interpreta SortCriteria. Solo se ordena por título; cualquier otro
// criterio se ignora y se devuelve el orden por defecto: rechazar una
// ordenación que un televisor pide siempre dejaría la biblioteca vacía.
func parseSort(sc string) (desc bool) {
	for part := range strings.SplitSeq(sc, ",") {
		part = strings.TrimSpace(part)
		if part == "-dc:title" {
			return true
		}
		if part == "+dc:title" || part == "dc:title" {
			return false
		}
	}
	return false
}

func parseIndex(v string, def int64) (int64, bool) {
	if strings.TrimSpace(v) == "" {
		return def, true
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 || n > 1<<31 {
		return 0, false
	}
	return n, true
}
