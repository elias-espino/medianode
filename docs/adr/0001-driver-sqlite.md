# 0001. Driver SQLite: modernc.org/sqlite

**Estado:** aceptada, con comparación pendiente\
**Fecha:** 2 de noviembre de 2026\
**Validación:** V1

## Decisión

Usar `modernc.org/sqlite` (Go puro, sin CGO).

## Resultados

Con v1.61.1 (SQLite 3.54.1), Go 1.27.3 en Windows, y el DSN de la Fase 2:

| Prueba | Resultado |
|--------|-----------|
| 100 000 inserciones en lotes de 500 | 2,46 s (40 663 filas/s) |
| Listado paginado con escritor activo | Media 0,29 ms, máximo 20,9 ms |
| 50 goroutines escribiendo a la vez | 0 errores |
| FTS5 sin acentos: `pelicula` encuentra `Película` | Correcto |
| `VACUUM INTO` con WAL | Correcto |
| Lector con `query_only` | Rechaza escrituras |

El DSN con `_pragma`, `_txlock=immediate` y `query_only` funciona tal como
lo define el plan.

## Pendiente

No se comparó con `mattn/go-sqlite3`, porque este equipo no tiene compilador
de C. Los resultados absolutos superan lo que necesita un servidor doméstico,
así que la comparación deja de bloquear el avance. Conviene repetir la prueba
en CI o en hardware modesto (NAS, Raspberry Pi).
