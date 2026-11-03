# 0002. SQLC y FTS5

**Estado:** aceptada\
**Fecha:** 2 de noviembre de 2026\
**Validación:** V2

## Decisión

Mantener `media_search` en las migraciones, pero escribir con `database/sql`
las consultas que usan `MATCH`.

## Resultados

Con sqlc 1.31.1 y el esquema completo del documento maestro:

- `CREATE VIRTUAL TABLE ... USING fts5` se acepta, y genera el modelo
  `MediaSearch`.
- `WHERE media_search MATCH ?` falla con
  `column "media_search" does not exist`.
- `WHERE type = ?1` en un `DELETE` con subconsulta falla con
  `column reference "type" is ambiguous`. Se resuelve escribiendo
  `media_items.type`.
- Con esos dos cambios, el resto de consultas del plan generan código.

## Consecuencias

`SearchItems` vive escrita a mano en `internal/readmodel`. Es la única
consulta fuera de SQLC por ahora, y sus pruebas de integración cubren lo que
SQLC no comprueba.
