# 0005. Proveedor de metadatos

**Estado:** aceptada\
**Fecha:** 3 de noviembre de 2026\
**Validación:** §26.4 del documento maestro

## Decisión

TMDB (API v3) es el primer y único adaptador de `metadata.Provider`. Cubre
películas y series, imágenes y clasificaciones por país en una sola API.

## Condiciones consultadas

Fuentes, leídas el 3 de noviembre de 2026:

- Condiciones de uso de la API, actualizadas el 20 de octubre de 2023:
  <https://www.themoviedb.org/api-terms-of-use>
- Preguntas frecuentes: <https://developer.themoviedb.org/docs/faq>
- Límites de peticiones: <https://developer.themoviedb.org/docs/rate-limiting>
- Logotipos: <https://www.themoviedb.org/about/logos-attribution>

| Tema | Condición | Cómo se cumple |
|------|-----------|----------------|
| Licencia | Gratis para uso no comercial con atribución; el comercial exige un acuerdo escrito | MediaNode no cobra ni genera ingresos. Una distribución comercial necesitaría ese acuerdo |
| Clave | Licencia intransferible y no sublicenciable | El servidor no incluye ninguna clave: cada instalación usa la suya (`metadata.tmdb_api_key` o `MEDIANODE_METADATA_TMDB_API_KEY`) |
| Atribución | Aviso «uses TMDB and the TMDB APIs but is not endorsed…» y logotipo de TMDB, menos destacado que el propio | El panel muestra el aviso cuando el proveedor es TMDB. **Falta el logotipo** |
| Caché | No guardar información de TMDB más de 6 meses | El Enricher renueva a los 150 días los metadatos y las imágenes de lo que no está bloqueado |
| Imágenes | Prohibido crear derivados del contenido | Se guardan tal cual, en el tamaño que sirve TMDB; no se redimensionan |
| Límite | Unas 40 peticiones por segundo; 429 al superarlo | El adaptador limita a 10 por segundo y trata 429 como fallo pasajero |
| Aprendizaje automático | Prohibido usar el contenido para entrenar modelos | MediaNode no lo hace |

## Consecuencias

- Sin clave, el catálogo funciona con los datos de los archivos y se edita a
  mano.
- Un elemento bloqueado conserva lo que fijó el administrador y no se renueva.
  Si esos datos procedían de TMDB, siguen sujetos al límite de 6 meses: la
  decisión de bloquearlos es del administrador.
- Las condiciones no dicen nada sobre servidores autoalojados ni de código
  abierto. La lectura de este ADR (una clave por instalación) es una
  interpretación, no una regla explícita.
- Falta confirmar en la documentación de la API que la clave v3 por parámetro
  sigue admitida junto al token v4; el adaptador acepta ambas.
