# Registro de cambios

Formato libre, lo más reciente primero. Mientras no haya versiones publicadas, todo va
bajo «Sin publicar». Lo que no se probó en hardware real se dice.

## Sin publicar

### 2026-11-05

**Corregido**

- **Reproducción en el navegador.** La política de seguridad del panel bloqueaba las URL
  `blob:` que usa hls.js, así que los vídeos que se remuxean o transcodifican no
  arrancaban. Se añaden `media-src 'self' blob:` y `worker-src 'self' blob:`
  (`web/embed.go`, con test). Los MP4 directos no se veían afectados. No se reprodujo un
  capítulo transcodificado en el navegador tras el cambio.
- **Temporadas añadidas después.** Una serie ya enriquecida no pedía a TMDB los datos de
  una temporada nueva hasta el refresco periódico, y la temporada aparecía sin títulos.
  Ahora el escáner devuelve la serie a la cola de metadatos cuando crea una temporada o un
  episodio (salvo que esté bloqueada), reutilizando el identificador ya conocido
  (`internal/scanner`, con test).
- **Series duplicadas.** El escáner agrupa por nombre de carpeta; dos carpetas con nombres
  distintos creaban dos series aunque TMDB las identificara como la misma
  (`Mr.Robot.SEASON.01…` y `Mr Robot (2015)`). Ahora las que comparten identificador del
  proveedor se unen en la más antigua, que recibe las temporadas y episodios de la otra,
  adopta la clave de la carpeta nueva y vuelve a la cola. Ocurre al identificar o
  refrescar y en cada pasada de metadatos, así que también arregla las ya existentes
  (`internal/metadata/merge.go`, con tests). Límites: solo series, no películas; del
  episodio que estaba en las dos se conserva uno y se pierde el progreso del otro. No se
  probó sobre una base de datos real.

**Documentación**

- Capturas del panel con un servidor de demostración en `docs/screenshots/`, enlazadas
  desde el README. Las rutas locales y las IP privadas se enmascararon.
