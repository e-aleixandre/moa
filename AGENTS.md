# moa — guía para agentes

moa es un coding-agent harness en Go. La lógica vive en `pkg/` sobre un **bus de eventos**;
encima hay **dos frontends** que comparten esa lógica:

- **TUI** (`pkg/tui/`) — interfaz de terminal (bubbletea), punto de entrada local.
- **Web / serve** (`pkg/serve/` + `pkg/serve/frontend/`) — servidor HTTP/WS + SPA (Preact),
  lo que se usa desde el móvil vía Tailscale.

El core (agente, providers, sesión, bus, tools) es común. Los dos frontends son solo presentación.

## Regla de PARIDAD (importante)

Toda feature de cara al usuario —cualquier cosa de **"cómo se muestra"** o interacción— debe
implementarse en **AMBAS** capas, TUI y web, en el mismo cambio. No dejar una capa adelantada y
la otra atrás: eso genera divergencia y deuda.

- La lógica/datos compartidos van en un paquete de `pkg/` reutilizable por ambas capas
  (ejemplo: `pkg/usage` alimenta el segmento de la statusline de la TUI **y** el endpoint
  `/api/usage` + widget del frontend web).
- Si una feature solo aplica a una capa, decláralo explícitamente y justifícalo.

## Regla de DOCUMENTACIÓN

`docs/` es la fuente de verdad de la documentación y se publica tal cual en
**letmoa.run/docs** (el sitio la sincroniza desde el último tag de release; el markdown
no se edita en ningún otro sitio). Por eso la doc viaja **en el mismo commit** que el
código, igual que la regla de paridad:

- Feature de cara al usuario ⇒ actualiza la página de `docs/` que le corresponda.
- Flag, tool, campo de config, slash command o alias de modelo nuevo ⇒ su fila en
  `docs/cli.md`, `docs/tools.md`, `docs/configuration.md` o `docs/tui.md`.
  `internal/docsdrift` compara esos conjuntos con el código y **falla el build** si falta
  alguno; compara claves, no prosa, así que reescribir una descripción es libre.
- Página nueva en `docs/` ⇒ alta también en `docs.manifest.mjs` del repo `moa-landing`,
  o no se publica (el sync lo avisa por consola).
- Enlaces relativos entre docs (`./serve.md`, `../CHANGELOG.md`) e imágenes en
  `docs/assets/`: el sync los reescribe solo. Escríbelos como se leen bien en GitHub.

## Build / test / deploy

- Backend: `go build ./...` · `go vet ./...` · `go test ./...` (usa `-race` en paquetes con concurrencia).
- Frontend: `cd pkg/serve/frontend && node esbuild.mjs` (en la VM `dev` no hay npm/make: `bun esbuild.mjs`).
  El output va embebido en `pkg/serve/static/` (via `//go:embed`), así que **rebuild del frontend antes de compilar el binario** si tocas el frontend.
- Formatea con `gofmt` los ficheros que crees/edites. (Aviso: algunos ficheros del repo ya vienen
  gofmt-unclean de antes —p. ej. `pkg/tui/app.go`, `cmd/moa/main.go`—; no los reformatees en bloque
  dentro de un cambio de feature, mete ruido en el review.)

## Convenciones

- Cambios quirúrgicos: toca solo lo necesario, no refactorices lo que no está roto.
- La ruta `serve` no tiene auth por defecto; el límite de seguridad recomendado sigue siendo Tailscale/localhost. Existe auth opt-in con `--token`/`MOA_SERVE_TOKEN` (cookie de sesión + check de `Host` anti-rebinding + CSRF por header) — ver `docs/serve.md#security`. Nunca exponer el puerto a Internet sin `--token`.
- Al añadir un evento/estado que el frontend debe ver, sigue el patrón existente end-to-end
  (`ContextUpdated`: evento en `pkg/bus` → reactor en `handlers.go` → traducción en `pkg/serve/ws.go` →
  `case` en `frontend/src/api.js` → handler en `ws-handlers.js` → componente). Para datos **globales**
  (no por-sesión), prefiere un endpoint REST + polling ligero en el frontend en vez del bus por-sesión.
