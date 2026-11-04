package routes

import (
	"context"
	"testing"

	"medianode/internal/playback"
)

// planDLNA pide directamente el plan de entrega de un archivo, con la política
// de subtítulos que se indique.
func (e *env) planDLNA(item, file, mode string, burn bool) *playback.DLNAPlan {
	e.t.Helper()
	plan, err := e.playback.PlanDLNA(context.Background(), item, file, playback.DLNAOptions{Mode: mode, BurnSubtitles: burn, Language: "es-ES"})
	if err != nil {
		e.t.Fatal(err)
	}
	return plan
}

func TestDLNAPoliticaDeSubtitulos(t *testing.T) {
	video := streamRow{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}
	spaAudio := func(sub streamRow) []streamRow {
		return []streamRow{video, {typ: "audio", codec: "aac", lang: "spa"}, sub}
	}
	engAudio := func(sub streamRow) []streamRow {
		return []streamRow{video, {typ: "audio", codec: "aac", lang: "eng"}, sub}
	}
	forced := streamRow{typ: "subtitle", codec: "subrip", lang: "spa", forced: true}
	plain := streamRow{typ: "subtitle", codec: "subrip", lang: "spa"}
	cases := []struct {
		name       string
		streams    []streamRow
		mode       string
		burn       bool
		wantDirect bool
		wantBurn   bool
	}{
		{"audio en el idioma preferido con un subtítulo forzado: se graba", spaAudio(forced), "auto", true, false, true},
		{"con burn_subtitles off no se graba y va directo", spaAudio(forced), "auto", false, true, false},
		{"audio en el idioma preferido y subtítulo normal: ni se graba", spaAudio(plain), "auto", true, true, false},
		{"audio de otro idioma: se graba el subtítulo del idioma preferido", engAudio(plain), "auto", true, false, true},
		{"audio de otro idioma y burn_subtitles off: directo", engAudio(plain), "auto", false, true, false},
		{"modo direct: nunca se recodifica", spaAudio(forced), "direct", true, true, false},
		{"modo transcode con un subtítulo forzado: se graba", spaAudio(forced), "transcode", true, false, true},
		{"modo transcode sin subtítulos grabados: sigue por MPEG-TS", spaAudio(plain), "transcode", true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, m := dlnaEnv(t, c.mode, 1, 1)
			file := m.addDLNAFile(m.item, "x.mkv", "mkv_srt_internal.mkv", "mp4", 4000, c.streams)
			plan := e.planDLNA(m.item, file, c.mode, c.burn)
			if plan.Direct != c.wantDirect {
				t.Fatalf("Direct = %v, se esperaba %v (%+v)", plan.Direct, c.wantDirect, plan.Decision)
			}
			if plan.BurnedSubtitle != c.wantBurn {
				t.Fatalf("BurnedSubtitle = %v, se esperaba %v", plan.BurnedSubtitle, c.wantBurn)
			}
			if c.wantBurn && (plan.Pipe.Burn == 0 || plan.Pipe.BurnFile == "" || plan.Pipe.Kind != "heavy") {
				t.Fatalf("el plan de FFmpeg no graba el subtítulo: %+v", plan.Pipe)
			}
			if !plan.Direct && plan.Pipe.Path == "" {
				t.Fatalf("falta la ruta del archivo: %+v", plan.Pipe)
			}
		})
	}
}

func TestDLNAUnSubtituloQueNoSePuedeGrabarNoImpideReproducir(t *testing.T) {
	// Sin FFmpeg no hay burn-in: el archivo se entrega tal cual, con o sin subtítulo.
	e, m := dlnaEnv(t, "auto", 0, 0)
	file := m.addDLNAFile(m.item, "x.mkv", "mkv_srt_internal.mkv", "mp4", 4000,
		[]streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}, {typ: "subtitle", codec: "subrip", lang: "spa", forced: true}})
	plan := e.planDLNA(m.item, file, "auto", true)
	if !plan.Direct || plan.BurnedSubtitle {
		t.Fatalf("%+v", plan)
	}
}

func TestDLNAElPerfilGenericoDecideLoQueSeRecodifica(t *testing.T) {
	cases := []struct {
		name       string
		container  string
		streams    []streamRow
		wantDirect bool
		wantKind   string // De la tubería, si no es directo.
	}{
		{"MP4 H.264 y AAC", "mp4", h264aac, true, ""},
		{"MPEG-TS H.264 y AAC", "ts", h264aac, true, ""},
		{"MKV H.264 y AAC: se copia y se empaqueta en MPEG-TS", "mkv", h264aac, false, "light"},
		{"MP4 con HEVC: se recodifica", "mp4", hevcaac, false, "heavy"},
		{"MP4 H.264 con AC-3 de 6 canales: el vídeo se copia y el audio se recodifica", "mp4",
			[]streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "ac3", lang: "spa", channels: 6}}, false, "light"},
		{"MP4 de 4K: se recodifica a 1080p", "mp4",
			[]streamRow{{typ: "video", codec: "h264", w: 3840, h: 2160, depth: 8}, {typ: "audio", codec: "aac", lang: "spa"}}, false, "heavy"},
		{"MP4 H.264 de 10 bits: se recodifica", "mp4",
			[]streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 10}, {typ: "audio", codec: "aac", lang: "spa"}}, false, "heavy"},
		{"MP4 con audio DTS: se recodifica el audio", "mp4",
			[]streamRow{{typ: "video", codec: "h264", w: 640, h: 360, depth: 8}, {typ: "audio", codec: "dts", lang: "spa"}}, false, "light"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, m := dlnaEnv(t, "auto", 1, 1)
			file := m.addDLNAFile(m.item, "x.mkv", "mkv_h264_aac.mkv", c.container, 4000, c.streams)
			plan := e.planDLNA(m.item, file, "auto", true)
			if plan.Direct != c.wantDirect {
				t.Fatalf("Direct = %v (%+v)", plan.Direct, plan.Decision)
			}
			if !plan.Direct && string(plan.Pipe.Kind) != c.wantKind {
				t.Fatalf("clase de trabajo = %q, se esperaba %q", plan.Pipe.Kind, c.wantKind)
			}
			if c.wantKind == "heavy" && (plan.Pipe.Height == 0 || plan.Pipe.Height > 1080 || plan.Pipe.VideoBitrate == 0) {
				t.Fatalf("salida %dp %d: %+v", plan.Pipe.Height, plan.Pipe.VideoBitrate, plan.Pipe)
			}
		})
	}
}

func TestDLNAUnArchivoQueNoEsDelElementoNoSePlanifica(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 1, 1)
	m.addDLNAFile(m.item, "a.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	other := m.addMovie("Otra")
	f := m.addDLNAFile(other, "b.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	if _, err := e.playback.PlanDLNA(context.Background(), m.item, f, playback.DLNAOptions{Mode: "auto"}); err == nil {
		t.Fatal("un archivo de otro elemento no debe planificarse")
	}
	if _, err := e.playback.PlanDLNA(context.Background(), m.item, "no-existe", playback.DLNAOptions{Mode: "auto"}); err == nil {
		t.Fatal("un archivo inexistente no debe planificarse")
	}
}

func TestDLNANoPlanificaLoQueNoSePuedeReproducir(t *testing.T) {
	e, m := dlnaEnv(t, "auto", 1, 1)
	pending := m.addDLNAFile(m.item, "a.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	m.exec("UPDATE media_files SET probe_status = 'pending' WHERE id = ?", pending)
	if _, err := e.playback.PlanDLNA(context.Background(), m.item, pending, playback.DLNAOptions{Mode: "auto"}); err == nil {
		t.Fatal("un archivo sin analizar no debe planificarse")
	}
	lost := m.addDLNAFile(m.item, "b.mp4", "mp4_h264_aac.mp4", "mp4", 4000, h264aac)
	m.exec("UPDATE media_files SET missing_since = '2026-01-01T00:00:00.000Z' WHERE id = ?", lost)
	if _, err := e.playback.PlanDLNA(context.Background(), m.item, lost, playback.DLNAOptions{Mode: "auto"}); err == nil {
		t.Fatal("un archivo perdido no debe planificarse")
	}
}

func TestDLNAUnArchivoSinVideoSeEntregaTalCual(t *testing.T) {
	// Sin pista de vídeo no hay nada que recodificar ni copiar a MPEG-TS: se
	// entrega el original, sin que el plan falle ni se caiga.
	for _, mode := range []string{"auto", "transcode"} {
		e, m := dlnaEnv(t, mode, 1, 1)
		file := m.addDLNAFile(m.item, "solo-audio.mp4", "mp4_h264_aac.mp4", "mp4", 4000, []streamRow{{typ: "audio", codec: "aac", lang: "spa"}})
		plan := e.planDLNA(m.item, file, mode, true)
		if !plan.Direct {
			t.Fatalf("modo %s: un archivo sin vídeo debe entregarse tal cual: %+v", mode, plan.Decision)
		}
	}
}

func TestDLNAUnHDRSeConvierteASDRSiSePuede(t *testing.T) {
	hdr := []streamRow{{typ: "video", codec: "hevc", w: 1280, h: 720, depth: 10, hdr: "hdr10"}, {typ: "audio", codec: "aac", lang: "spa"}}
	e, m := dlnaEnv(t, "auto", 1, 1)
	file := m.addDLNAFile(m.item, "hdr.mp4", "mp4_h264_aac.mp4", "mp4", 4000, hdr)

	plan := e.planDLNA(m.item, file, "auto", true)
	if plan.Direct || plan.Pipe.Kind != "heavy" || plan.Pipe.ToneMap == "" {
		t.Fatalf("un HDR debe pasar a SDR con tone mapping: %+v", plan.Pipe)
	}
	// Si este FFmpeg no puede convertirlo (o la política lo desactiva), se entrega
	// el original: un televisor con HDR lo mostrará, y uno sin él, al menos algo.
	e.playback.Features.ToneMapping = false
	plan = e.planDLNA(m.item, file, "auto", true)
	if !plan.Direct {
		t.Fatalf("sin tone mapping se entrega tal cual: %+v", plan.Pipe)
	}
}
