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

## Build / test / deploy

- Backend: `go build ./...` · `go vet ./...` · `go test ./...` (usa `-race` en paquetes con concurrencia).
- Frontend: `cd pkg/serve/frontend && node esbuild.mjs` (en la VM `dev` no hay npm/make: `bun esbuild.mjs`).
  El output va embebido en `pkg/serve/static/` (via `//go:embed`), así que **rebuild del frontend antes de compilar el binario** si tocas el frontend.
- Formatea con `gofmt` los ficheros que crees/edites. (Aviso: algunos ficheros del repo ya vienen
  gofmt-unclean de antes —p. ej. `pkg/tui/app.go`, `cmd/agent/main.go`—; no los reformatees en bloque
  dentro de un cambio de feature, mete ruido en el review.)

## Convenciones

- Cambios quirúrgicos: toca solo lo necesario, no refactorices lo que no está roto.
- La ruta `serve` NO tiene auth propia; el límite de seguridad es Tailscale. Nunca exponer el puerto a Internet.
- Al añadir un evento/estado que el frontend debe ver, sigue el patrón existente end-to-end
  (`ContextUpdated`: evento en `pkg/bus` → reactor en `handlers.go` → traducción en `pkg/serve/ws.go` →
  `case` en `frontend/src/api.js` → handler en `ws-handlers.js` → componente). Para datos **globales**
  (no por-sesión), prefiere un endpoint REST + polling ligero en el frontend en vez del bus por-sesión.
