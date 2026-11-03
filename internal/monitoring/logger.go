// Package monitoring reúne los logs estructurados y el diagnóstico.
package monitoring

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	logFileName   = "medianode.log"
	logMaxSizeMB  = 50
	logMaxBackups = 5
	logMaxAgeDays = 30
)

// LogConfig define dónde y con qué nivel se registra.
type LogConfig struct {
	Dir     string
	Level   string
	Console io.Writer
}

// NewLogger crea un logger JSON que escribe en un archivo con rotación y en la
// consola. El io.Closer devuelto cierra el archivo al apagar.
func NewLogger(cfg LogConfig) (zerolog.Logger, io.Closer, error) {
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		return zerolog.Nop(), nil, fmt.Errorf("nivel de log: %w", err)
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return zerolog.Nop(), nil, fmt.Errorf("crear directorio de logs: %w", err)
	}

	file := &lumberjack.Logger{
		Filename:   filepath.Join(cfg.Dir, logFileName),
		MaxSize:    logMaxSizeMB,
		MaxBackups: logMaxBackups,
		MaxAge:     logMaxAgeDays,
		Compress:   true,
	}

	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.TimestampFunc = func() time.Time { return time.Now().UTC() }

	var out io.Writer = file
	if cfg.Console != nil {
		out = io.MultiWriter(file, cfg.Console)
	}
	return zerolog.New(out).Level(level).With().Timestamp().Logger(), file, nil
}

// Module devuelve un logger que identifica al módulo emisor.
func Module(log zerolog.Logger, name string) zerolog.Logger {
	return log.With().Str("module", name).Logger()
}
