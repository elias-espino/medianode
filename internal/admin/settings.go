package admin

import (
	"encoding/json"

	"gopkg.in/yaml.v3"

	"medianode/internal/monitoring"
)

// hidden es lo que se muestra en lugar de un secreto configurado.
const hidden = "(configurada)"

// diskSpace aísla la dependencia de monitoring para las comprobaciones.
func diskSpace(path string) (free, total uint64, err error) { return monitoring.DiskSpace(path) }

// Settings devuelve la configuración efectiva tal como la escribiría
// server.yaml, sin secretos: la clave de TMDB se sustituye por un aviso de que
// está configurada. Es de solo lectura: cambiarla exige editar el archivo y
// reiniciar.
func (s *Service) Settings() (map[string]any, error) {
	c := s.Config
	if c.Metadata.TMDBAPIKey != "" {
		c.Metadata.TMDBAPIKey = hidden
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	// De YAML a JSON: las claves de los mapas anidados ya son cadenas con v3.
	out, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	return result, json.Unmarshal(out, &result)
}
