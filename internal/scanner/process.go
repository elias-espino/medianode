package scanner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/probe"
	"medianode/internal/storage"
)

// hashChunk es lo que se lee de cada extremo para la huella parcial.
const hashChunk = 1 << 20

type stats struct {
	mu         sync.Mutex
	discovered int64
	processed  int64
	changed    bool
}

func (st *stats) add(processed int64, changed bool) {
	st.mu.Lock()
	st.processed += processed
	st.changed = st.changed || changed
	st.mu.Unlock()
}

// pathScan es el estado de una raíz accesible tras el recorrido.
type pathScan struct {
	lp     sqlc.LibraryPath
	walked *walkResult
}

// task es un archivo nuevo o modificado que hay que procesar.
type task struct {
	lp    sqlc.LibraryPath
	f     found
	known *sqlc.ListPathFilesRow // nil si es nuevo
}

type outcome struct {
	task
	hash     string
	moveFrom string // id de un archivo ausente con la misma huella
	res      *probe.Result
	probeErr error
}

// scanLibrary trabaja en dos fases. Primero recorre todas las raíces y marca
// las ausencias; después procesa lo nuevo. Así un archivo movido entre dos
// raíces de la misma biblioteca ya figura como ausente cuando aparece en la
// otra, y se reconoce como movimiento.
func (s *Scanner) scanLibrary(ctx context.Context, job sqlc.ScanJob, st *stats, log zerolog.Logger) error {
	lib, err := s.db.R.GetLibrary(ctx, job.LibraryID)
	if err != nil {
		return fmt.Errorf("leer biblioteca: %w", err)
	}
	paths, err := s.db.R.ListLibraryPaths(ctx, lib.ID)
	if err != nil {
		return fmt.Errorf("leer rutas: %w", err)
	}

	var scanned []pathScan
	var tasks []task
	for _, lp := range paths {
		ps, pathTasks, err := s.reconcilePath(ctx, lp, st, log)
		if err != nil {
			return err
		}
		if ps != nil {
			scanned = append(scanned, *ps)
			tasks = append(tasks, pathTasks...)
		}
	}
	st.mu.Lock()
	for _, ps := range scanned {
		st.discovered += int64(len(ps.walked.videos))
	}
	st.mu.Unlock()

	if err := s.process(ctx, job, media.LibraryType(lib.Type), tasks, st, log); err != nil {
		return err
	}
	if err := s.identifyPending(ctx, lib, st, log); err != nil {
		return err
	}
	for _, ps := range scanned {
		if err := s.syncSubtitles(ctx, ps, st); err != nil {
			return err
		}
		if err := s.db.Q.SetLibraryPathScanned(ctx, sqlc.SetLibraryPathScannedParams{
			ID: ps.lp.ID, LastScanAt: new(s.timestamp()),
		}); err != nil {
			return fmt.Errorf("actualizar ruta: %w", err)
		}
	}

	files, items, err := s.Purge(ctx)
	if err != nil {
		return err
	}
	if files > 0 || items > 0 {
		st.add(0, true)
		log.Info().Int64("archivos", files).Int64("elementos", items).Msg("purgados tras la retención")
	}
	return nil
}

// identifyPending identifica los archivos ya analizados que aún no tienen
// elemento: un escaneo salta los que no cambiaron, así que sin esto un
// archivo que el identificador no entendía lo seguiría sin entender tras
// mejorar sus reglas. No lanza FFprobe: el análisis ya está guardado.
func (s *Scanner) identifyPending(ctx context.Context, lib sqlc.Library, st *stats, log zerolog.Logger) error {
	rows, err := s.db.R.ListUnidentifiedFiles(ctx, lib.ID)
	if err != nil {
		return fmt.Errorf("leer archivos sin identificar: %w", err)
	}
	done, left := 0, 0
	for start := 0; start < len(rows); start += s.opts.BatchSize {
		batch := rows[start:min(start+s.opts.BatchSize, len(rows))]
		err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
			for _, r := range batch {
				now := s.timestamp()
				if err := identifyTx(ctx, q, media.LibraryType(lib.Type), lib.ID, r.ID, r.RelativePath, r.DurationMs, now); err != nil {
					return err
				}
				links, err := q.ListFileItemIDs(ctx, r.ID)
				if err != nil {
					return err
				}
				if len(links) > 0 {
					done++
				} else {
					left++
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("identificar archivos: %w", err)
		}
	}
	if done > 0 {
		st.add(0, true)
	}
	if len(rows) > 0 {
		log.Info().Int("identificados", done).Int("sin_identificar", left).Msg("identificación de archivos ya analizados")
	}
	return nil
}

// reconcilePath recorre una raíz, marca ausencias y reapariciones, y devuelve
// los archivos que hay que analizar. Una raíz inaccesible devuelve nil y no
// toca ningún archivo.
func (s *Scanner) reconcilePath(ctx context.Context, lp sqlc.LibraryPath, st *stats, log zerolog.Logger) (*pathScan, []task, error) {
	log = log.With().Str("library_path_id", lp.ID).Logger()
	known, err := s.db.R.ListPathFiles(ctx, lp.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("leer archivos conocidos: %w", err)
	}

	var walked *walkResult
	available := storage.RootAvailable(lp.Path)
	if available {
		walked, err = walk(ctx, lp.Path, s.opts.MaxDepth, log)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		// El disco puede desconectarse durante el recorrido.
		available = err == nil && storage.RootAvailable(lp.Path)
	}
	if available && len(walked.videos) == 0 && hasPresent(known) {
		// Un punto de montaje sin su disco aparece como carpeta vacía. Tomarlo
		// por bueno marcaría ausente toda la biblioteca.
		if empty, _ := storage.RootEmpty(lp.Path); empty {
			available = false
		}
	}
	if err := s.setAvailable(ctx, lp, available, log); err != nil {
		return nil, nil, err
	}
	if !available {
		return nil, nil, nil
	}

	byPath := make(map[string]*sqlc.ListPathFilesRow, len(known))
	for i := range known {
		byPath[known[i].RelativePath] = &known[i]
	}
	var tasks []task
	var reappeared []string
	for _, f := range walked.videos {
		k, ok := byPath[f.rel]
		if !ok {
			tasks = append(tasks, task{lp: lp, f: f})
			continue
		}
		delete(byPath, f.rel)
		unchanged := k.FileSize != nil && *k.FileSize == f.size &&
			k.ModifiedAt != nil && *k.ModifiedAt == f.mtime &&
			k.ProbeStatus != string(media.ProbePending)
		if !unchanged {
			tasks = append(tasks, task{lp: lp, f: f, known: k})
		} else if k.MissingSince != nil {
			reappeared = append(reappeared, k.ID)
		}
	}
	var missing []string
	for rel, k := range byPath {
		if k.MissingSince == nil && !underAny(rel, walked.failed) {
			missing = append(missing, k.ID)
		}
	}
	if len(missing) > 0 || len(reappeared) > 0 {
		if err := s.markPresence(ctx, missing, reappeared); err != nil {
			return nil, nil, err
		}
		st.add(0, true)
		log.Info().Int("ausentes", len(missing)).Int("reaparecidos", len(reappeared)).Msg("presencia actualizada")
	}
	return &pathScan{lp: lp, walked: walked}, tasks, nil
}

func hasPresent(known []sqlc.ListPathFilesRow) bool {
	for _, k := range known {
		if k.MissingSince == nil {
			return true
		}
	}
	return false
}

func (s *Scanner) setAvailable(ctx context.Context, lp sqlc.LibraryPath, available bool, log zerolog.Logger) error {
	want := int64(0)
	if available {
		want = 1
	}
	if lp.IsAvailable == want {
		return nil
	}
	if !available {
		log.Warn().Msg("raíz inaccesible: no se modifica ningún archivo")
	}
	err := s.db.Q.SetLibraryPathAvailable(ctx, sqlc.SetLibraryPathAvailableParams{ID: lp.ID, IsAvailable: want})
	if err != nil {
		return fmt.Errorf("actualizar disponibilidad: %w", err)
	}
	return nil
}

func (s *Scanner) markPresence(ctx context.Context, missing, reappeared []string) error {
	now := s.timestamp()
	apply := func(ids []string, fn func(q *sqlc.Queries, id string) error) error {
		for _, chunk := range chunks(ids, s.opts.BatchSize) {
			err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
				for _, id := range chunk {
					if err := fn(q, id); err != nil {
						return fmt.Errorf("actualizar presencia: %w", err)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	err := apply(missing, func(q *sqlc.Queries, id string) error {
		return q.MarkFileMissing(ctx, sqlc.MarkFileMissingParams{ID: id, MissingSince: &now})
	})
	if err != nil {
		return err
	}
	return apply(reappeared, func(q *sqlc.Queries, id string) error {
		return q.ClearFileMissing(ctx, sqlc.ClearFileMissingParams{ID: id, UpdatedAt: now})
	})
}

// process analiza las tareas con varios trabajadores y las guarda por lotes,
// cada uno en una transacción corta.
func (s *Scanner) process(ctx context.Context, job sqlc.ScanJob, libType media.LibraryType, tasks []task, st *stats, log zerolog.Logger) error {
	if len(tasks) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	work := make(chan task)
	results := make(chan outcome, s.opts.BatchSize)
	var wg sync.WaitGroup
	for range s.opts.Workers {
		wg.Go(func() {
			for t := range work {
				o, err := s.prepare(ctx, libType, t, log)
				if err != nil {
					cancel(err)
					return
				}
				if o != nil {
					select {
					case results <- *o:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	go func() {
		defer close(work)
		for _, t := range tasks {
			select {
			case work <- t:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	batch := make([]outcome, 0, s.opts.BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.persist(ctx, libType, batch, log); err != nil {
			return err
		}
		st.add(int64(len(batch)), true)
		batch = batch[:0]
		st.mu.Lock()
		discovered, processed := st.discovered, st.processed
		st.mu.Unlock()
		err := s.db.Q.UpdateScanProgress(ctx, sqlc.UpdateScanProgressParams{
			ID: job.ID, FilesDiscovered: discovered, FilesProcessed: processed,
		})
		if err != nil {
			return fmt.Errorf("actualizar progreso: %w", err)
		}
		return nil
	}
	for o := range results {
		batch = append(batch, o)
		if len(batch) >= s.opts.BatchSize {
			if err := flush(); err != nil {
				cancel(err)
				for range results {
				}
				return err
			}
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return flush()
}

// prepare calcula la huella y, salvo que sea un movimiento, analiza el
// archivo. Devuelve nil si el archivo dejó de poder leerse: el siguiente
// escaneo lo reintentará. Un error detiene el escaneo.
func (s *Scanner) prepare(ctx context.Context, libType media.LibraryType, t task, log zerolog.Logger) (*outcome, error) {
	hash, err := partialHash(t.lp.Path, t.f.rel, t.f.size)
	if err != nil {
		log.Warn().Err(err).Str("ruta", t.f.abs).Msg("no se pudo leer el archivo")
		return nil, nil
	}
	o := &outcome{task: t, hash: hash}
	if t.known == nil {
		id, err := s.db.R.FindMoveCandidate(ctx, sqlc.FindMoveCandidateParams{
			LibraryID: t.lp.LibraryID, FileSize: &t.f.size, FileHash: &hash,
		})
		if err == nil {
			o.moveFrom = id
			return o, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("buscar movimiento: %w", err)
		}
	}
	o.res, o.probeErr = s.prober.Run(ctx, t.f.abs)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(o.probeErr, probe.ErrUnavailable):
		return nil, o.probeErr
	case o.probeErr != nil:
		log.Warn().Err(o.probeErr).Str("ruta", t.f.abs).Msg("análisis fallido, se marca como failed")
	}
	return o, nil
}

// partialHash es SHA-256 del tamaño, el primer MiB y el último MiB (§7.3).
func partialHash(root, rel string, size int64) (string, error) {
	f, err := storage.OpenMedia(root, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_ = binary.Write(h, binary.BigEndian, size)
	if _, err := io.CopyN(h, f, min(size, hashChunk)); err != nil {
		return "", fmt.Errorf("leer inicio: %w", err)
	}
	if size > hashChunk {
		start := max(size-hashChunk, hashChunk)
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return "", fmt.Errorf("leer final: %w", err)
		}
		if _, err := io.CopyN(h, f, size-start); err != nil {
			return "", fmt.Errorf("leer final: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Scanner) persist(ctx context.Context, libType media.LibraryType, batch []outcome, log zerolog.Logger) error {
	now := s.timestamp()
	return s.db.InTx(ctx, func(q *sqlc.Queries) error {
		for i := range batch {
			if err := s.persistOne(ctx, q, libType, &batch[i], now); err != nil {
				return fmt.Errorf("guardar %s: %w", batch[i].f.rel, err)
			}
		}
		return nil
	})
}

func (s *Scanner) persistOne(ctx context.Context, q *sqlc.Queries, libType media.LibraryType, o *outcome, now string) error {
	if o.moveFrom != "" {
		// Se vuelve a buscar dentro de la transacción: otro archivo del lote
		// puede haber reclamado ya el mismo ausente.
		id, err := q.FindMoveCandidate(ctx, sqlc.FindMoveCandidateParams{
			LibraryID: o.lp.LibraryID, FileSize: &o.f.size, FileHash: &o.hash,
		})
		if err == nil {
			return q.MoveMediaFile(ctx, sqlc.MoveMediaFileParams{
				ID: id, LibraryPathID: o.lp.ID, RelativePath: o.f.rel, ModifiedAt: &o.f.mtime, UpdatedAt: now,
			})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Sin candidato se guarda pendiente de análisis para el próximo escaneo.
		id, err = database.NewID()
		if err != nil {
			return err
		}
		return q.CreateMediaFile(ctx, sqlc.CreateMediaFileParams{
			ID: id, LibraryPathID: o.lp.ID, RelativePath: o.f.rel, FileSize: &o.f.size,
			ModifiedAt: &o.f.mtime, FileHash: &o.hash, ProbeStatus: string(media.ProbePending), CreatedAt: now,
		})
	}

	status := media.ProbeDone
	var container *string
	var duration, bitrate *int64
	if o.probeErr != nil {
		status = media.ProbeFailed
	} else {
		container = new(probe.Container(o.res.FormatName, filepath.Ext(o.f.rel)))
		duration, bitrate = o.res.DurationMS, o.res.Bitrate
	}

	fileID := ""
	if o.known == nil {
		id, err := database.NewID()
		if err != nil {
			return err
		}
		fileID = id
		err = q.CreateMediaFile(ctx, sqlc.CreateMediaFileParams{
			ID: id, LibraryPathID: o.lp.ID, RelativePath: o.f.rel, FileSize: &o.f.size,
			ModifiedAt: &o.f.mtime, FileHash: &o.hash, Container: container,
			DurationMs: duration, Bitrate: bitrate, ProbeStatus: string(status), CreatedAt: now,
		})
		if err != nil {
			return err
		}
	} else {
		fileID = o.known.ID
		err := q.UpdateMediaFileProbe(ctx, sqlc.UpdateMediaFileProbeParams{
			ID: fileID, FileSize: &o.f.size, ModifiedAt: &o.f.mtime, FileHash: &o.hash,
			Container: container, DurationMs: duration, Bitrate: bitrate,
			ProbeStatus: string(status), UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if err := q.DeleteFileStreams(ctx, fileID); err != nil {
			return err
		}
	}
	if status != media.ProbeDone {
		return nil
	}
	for _, st := range o.res.Streams {
		if err := insertStream(ctx, q, fileID, st); err != nil {
			return err
		}
	}
	// Un archivo modificado conserva su elemento; solo se identifica si aún no
	// tiene ninguno.
	links, err := q.ListFileItemIDs(ctx, fileID)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return identifyTx(ctx, q, libType, o.lp.LibraryID, fileID, o.f.rel, duration, now)
	}
	return nil
}

func insertStream(ctx context.Context, q *sqlc.Queries, fileID string, st probe.Stream) error {
	id, err := database.NewID()
	if err != nil {
		return err
	}
	return q.CreateMediaStream(ctx, sqlc.CreateMediaStreamParams{
		ID: id, MediaFileID: fileID, StreamIndex: st.Index, StreamType: string(st.Type),
		Codec: st.Codec, Profile: st.Profile, Level: st.Level, Language: st.Language,
		Channels: st.Channels, Width: st.Width, Height: st.Height, FrameRate: st.FrameRate,
		Bitrate: st.Bitrate, BitDepth: st.BitDepth, ColorTransfer: st.ColorTransfer,
		ColorPrimaries: st.ColorPrimaries, HdrFormat: st.HDRFormat,
		IsDefault: boolInt(st.Default), IsForced: boolInt(st.Forced), StreamMetadataJson: st.MetadataJSON,
	})
}

// identifyTx crea o reutiliza los elementos del archivo y los enlaza. Un
// archivo de serie sin patrón de episodio queda sin identificar.
func identifyTx(ctx context.Context, q *sqlc.Queries, libType media.LibraryType, libraryID, fileID, rel string, duration *int64, now string) error {
	if libType == media.LibraryMovies {
		g := ParseMovie(rel)
		id, err := topLevelItem(ctx, q, libraryID, media.ItemMovie, g.Title, g.Year, now)
		if err != nil {
			return err
		}
		return link(ctx, q, fileID, id, 0, duration)
	}

	g, ok := ParseEpisode(rel)
	if !ok {
		return nil
	}
	series, err := topLevelItem(ctx, q, libraryID, media.ItemSeries, g.Series, g.Year, now)
	if err != nil {
		return err
	}
	// Una temporada o un episodio nuevo deja los datos de la serie incompletos:
	// se piden de nuevo al proveedor, con el identificador ya conocido.
	grew := false
	season, err := q.GetSeason(ctx, sqlc.GetSeasonParams{ParentID: &series, SeasonNumber: &g.Season})
	if errors.Is(err, sql.ErrNoRows) {
		grew = true
		season, err = createItem(ctx, q, sqlc.CreateMediaItemParams{
			LibraryID: libraryID, ParentID: &series, Type: string(media.ItemSeason),
			Title: fmt.Sprintf("Temporada %d", g.Season), SeasonNumber: &g.Season, CreatedAt: now,
		})
	}
	if err != nil {
		return err
	}
	// La duración de un archivo con varios episodios no es la de ninguno.
	if len(g.Episodes) > 1 {
		duration = nil
	}
	for i, n := range g.Episodes {
		ep, err := q.GetEpisode(ctx, sqlc.GetEpisodeParams{ParentID: &season, EpisodeNumber: &n})
		if errors.Is(err, sql.ErrNoRows) {
			grew = true
			title := fmt.Sprintf("Episodio %d", n)
			if g.Title != "" {
				title = g.Title
			}
			sort := media.SortTitle(title, "")
			ep, err = createItem(ctx, q, sqlc.CreateMediaItemParams{
				LibraryID: libraryID, ParentID: &season, Type: string(media.ItemEpisode), Title: title,
				SortTitle: &sort, SeasonNumber: &g.Season, EpisodeNumber: &n, CreatedAt: now,
			})
		}
		if err != nil {
			return err
		}
		if err := link(ctx, q, fileID, ep, int64(i), duration); err != nil {
			return err
		}
	}
	if grew {
		return q.RequeueSeriesMetadata(ctx, sqlc.RequeueSeriesMetadataParams{ID: series, UpdatedAt: now})
	}
	return nil
}

// topLevelItem busca por match_key. Leer y crear dentro de la misma
// transacción es seguro porque solo hay un escritor.
func topLevelItem(ctx context.Context, q *sqlc.Queries, libraryID string, t media.ItemType, title string, year *int64, now string) (string, error) {
	key := MatchKey(title, year)
	id, err := q.GetTopLevelItem(ctx, sqlc.GetTopLevelItemParams{LibraryID: libraryID, Type: string(t), MatchKey: &key})
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	// El idioma de un nombre de archivo se desconoce.
	sort := media.SortTitle(title, "")
	return createItem(ctx, q, sqlc.CreateMediaItemParams{
		LibraryID: libraryID, Type: string(t), Title: title, SortTitle: &sort, Year: year,
		MatchKey: &key, CreatedAt: now,
	})
}

func createItem(ctx context.Context, q *sqlc.Queries, p sqlc.CreateMediaItemParams) (string, error) {
	id, err := database.NewID()
	if err != nil {
		return "", err
	}
	p.ID = id
	if err := q.CreateMediaItem(ctx, p); err != nil {
		return "", fmt.Errorf("crear elemento: %w", err)
	}
	return id, nil
}

func link(ctx context.Context, q *sqlc.Queries, fileID, itemID string, order int64, duration *int64) error {
	if err := q.LinkFileItem(ctx, sqlc.LinkFileItemParams{MediaFileID: fileID, MediaItemID: itemID, PartOrder: order}); err != nil {
		return fmt.Errorf("enlazar archivo: %w", err)
	}
	if duration != nil {
		if err := q.SetItemDurationIfEmpty(ctx, sqlc.SetItemDurationIfEmptyParams{ID: itemID, DurationMs: duration}); err != nil {
			return fmt.Errorf("guardar duración: %w", err)
		}
	}
	return nil
}

// syncSubtitles enlaza a cada video los subtítulos externos de su carpeta que
// empiezan por su nombre: «pelicula.es.forced.srt» para «pelicula.mkv».
func (s *Scanner) syncSubtitles(ctx context.Context, ps pathScan, st *stats) error {
	files, err := s.db.R.ListPathFiles(ctx, ps.lp.ID)
	if err != nil {
		return fmt.Errorf("leer archivos: %w", err)
	}
	existing, err := s.db.R.ListPathSubtitles(ctx, ps.lp.ID)
	if err != nil {
		return fmt.Errorf("leer subtítulos: %w", err)
	}
	type key struct{ fileID, rel string }
	have := map[key]string{}
	for _, e := range existing {
		have[key{e.MediaFileID, e.RelativePath}] = e.ID
	}

	ids := make(map[string]string, len(files))
	for _, f := range files {
		ids[f.RelativePath] = f.ID
	}
	var add []sqlc.CreateSubtitleParams
	for _, v := range ps.walked.videos {
		fileID, ok := ids[v.rel]
		if !ok {
			continue
		}
		base := stem(path.Base(v.rel))
		for _, sub := range ps.walked.subtitles[path.Dir(v.rel)] {
			info, ok := parseSubtitleName(base, path.Base(sub))
			if !ok {
				continue
			}
			k := key{fileID, sub}
			if _, ok := have[k]; ok {
				delete(have, k)
				continue
			}
			add = append(add, sqlc.CreateSubtitleParams{
				MediaFileID: fileID, RelativePath: sub, Language: info.language,
				Format: &info.format, IsDefault: boolInt(info.isDefault), IsForced: boolInt(info.forced),
			})
		}
	}
	// Lo que queda en have ya no existe junto a su video. Los de un video que no
	// se vio, por ausente o por estar en una carpeta ilegible, se conservan: un
	// subtítulo siempre comparte carpeta con su video.
	present := map[string]bool{}
	for _, v := range ps.walked.videos {
		present[ids[v.rel]] = true
	}
	var remove []string
	for k, id := range have {
		if present[k.fileID] {
			remove = append(remove, id)
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		for _, id := range remove {
			if err := q.DeleteSubtitle(ctx, id); err != nil {
				return fmt.Errorf("quitar subtítulo: %w", err)
			}
		}
		for _, p := range add {
			id, err := database.NewID()
			if err != nil {
				return err
			}
			p.ID = id
			if err := q.CreateSubtitle(ctx, p); err != nil {
				return fmt.Errorf("guardar subtítulo: %w", err)
			}
		}
		return nil
	})
	if err == nil {
		st.add(0, true)
	}
	return err
}

type subtitleInfo struct {
	language  string
	format    string
	forced    bool
	isDefault bool
}

// parseSubtitleName interpreta «base[.idioma][.forced|.default|.sdh].ext».
func parseSubtitleName(videoStem, name string) (subtitleInfo, bool) {
	ext := strings.ToLower(path.Ext(name))
	rest := strings.TrimSuffix(name, path.Ext(name))
	if rest != videoStem && !strings.HasPrefix(rest, videoStem+".") {
		return subtitleInfo{}, false
	}
	info := subtitleInfo{language: "und", format: strings.TrimPrefix(ext, ".")}
	for _, tok := range strings.Split(strings.TrimPrefix(rest[len(videoStem):], "."), ".") {
		t := strings.ToLower(tok)
		switch {
		case t == "":
		case t == "forced" || t == "forzados":
			info.forced = true
		case t == "default":
			info.isDefault = true
		case t == "sdh" || t == "cc" || t == "hi":
		case isLanguageCode(t):
			info.language = t
		}
	}
	return info, true
}

// isLanguageCode acepta códigos ISO 639-1 o 639-2, con región opcional
// («pt-br»).
func isLanguageCode(t string) bool {
	code, region, hasRegion := strings.Cut(t, "-")
	if len(code) < 2 || len(code) > 3 || (hasRegion && len(region) != 2) {
		return false
	}
	for _, r := range code + region {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func chunks(ids []string, size int) [][]string {
	var out [][]string
	for len(ids) > size {
		out = append(out, ids[:size])
		ids = ids[size:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}
