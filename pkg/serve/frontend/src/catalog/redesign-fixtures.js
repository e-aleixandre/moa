// redesign-fixtures — store-shaped sessions for the redesign lab. They are fed
// through the REAL drawerSessions() selector (chrome.js), so every row shows
// what production would show: brief first, path only as a fallback, "Needs
// you" derived from the pending permission, relative ages from `updated`.
//
// The catalog's own specimen.js rows are short English one-liners; the owner's
// real roster is long Spanish titles that truncate, one-line LLM briefs, and a
// few hundred saved sessions behind "4 open · 251 saved".

const now = Date.now();
const min = (n) => now - n * 60000;
const hour = (n) => now - n * 3600000;
const day = (n) => now - n * 86400000;

const HOME = "/home/ealeixandre/dev";

export const ACTIVE_ID = "rd-explore";

const OPEN = [
  {
    id: ACTIVE_ID,
    title: "Explorar tratamientos visuales del drawer y la pantalla de preview",
    state: "running",
    cwd: `${HOME}/moa/design-visual`,
    updated: min(2),
    briefAttempting: "Construir la vista de exploración en el design lab",
    briefProgress: "Lab levantado en el 7322, generando las capturas de cada tratamiento",
    runStartedAtMs: min(2),
  },
  {
    id: "rd-resume",
    title: "Arreglar el fallo al reanudar sesiones guardadas desde el móvil",
    state: "permission",
    cwd: `${HOME}/moa/fix-resume`,
    updated: min(3),
    pendingPerm: { id: "p1", tool_name: "bash", args: { command: "go test ./pkg/serve/..." } },
    briefProgress: "Reproducido con una sesión de 300 mensajes; el fix está en pruebas",
  },
  {
    id: "rd-deploy",
    title: "Deploy de pulse-api a staging",
    state: "running",
    cwd: `${HOME}/moa/pulse-api`,
    updated: min(1),
    briefAttempting: "Aplicar el chart en staging y verificar el rollout",
    runStartedAtMs: min(1),
  },
  {
    id: "rd-review",
    title: "Revisar el PR de split-handlers antes de mergear a main",
    state: "idle",
    unseen: true,
    cwd: `${HOME}/moa/main`,
    updated: min(46),
    briefProgress: "Tres comentarios menores; ninguno bloquea el merge",
  },
  {
    id: "rd-sqlite",
    title: "Migrar el store de sesiones a sqlite",
    state: "error",
    error: "provider 429 — reintentando (3/5)",
    cwd: `${HOME}/moa/migrate`,
    updated: min(18),
  },
  {
    id: "rd-cache",
    title: "Medir el hit rate de caché por proveedor",
    state: "idle",
    cwd: `${HOME}/moa/main`,
    updated: hour(3),
    briefProgress: "Anthropic 96,3 %, OpenAI 70,8 %; el informe está en tmp/cache-report.md",
  },
];

// Saved titles cycle through this list so the tail reads like a real history,
// not "session 1…251".
const SAVED_TITLES = [
  "Notas de diseño del verificador de entregas",
  "Perfilar la memoria de moa serve con pprof",
  "Quitar el header móvil y dejar el chip flotante",
  "Ajustar el copy de la barra de estado",
  "Refactor largo de los handlers de sesión",
  "Investigar el zoom de iOS al enfocar el composer",
  "Compactación: guardar el trabajo antes de resumir",
  "Wake-on-event: bandeja de entrada y toasts",
  "Tests de carrera en el bus de websocket",
  "Hacer el drawer agrupable por carpeta",
  "Live preview: proxy y dirección pública",
  "Subagentes persistentes en el dock",
  "Rewind desde los waypoints del usuario",
  "Auditoría de accesibilidad del menú de sesión",
  "Cambiar la fuente del código a Plex Mono",
  "Fallback del resumidor cuando el proveedor falla",
  "Modelo redirigido por cuota agotada",
  "TypeError en OrderSummary — 412 eventos",
  "Documentar el pipeline de release",
  "Pairing de Pulse desde el pie del drawer",
];

const SAVED_CWDS = [
  `${HOME}/moa/main`,
  `${HOME}/moa/main`,
  `${HOME}/moa/release-next`,
  `${HOME}/pulse`,
  `${HOME}/moa/main`,
  `${HOME}/tienda`,
];

function savedSessions(count) {
  const out = {};
  for (let i = 0; i < count; i++) {
    const id = `rd-saved-${i}`;
    out[id] = {
      id,
      title: SAVED_TITLES[i % SAVED_TITLES.length],
      state: "saved",
      cwd: SAVED_CWDS[i % SAVED_CWDS.length],
      // newest first: 1d, 1d, 2d, 3d… stretching to ~3 months
      updated: day(1 + Math.floor(i * 0.4)),
    };
  }
  return out;
}

export const SAVED_COUNT = 251;

export const REDESIGN_SESSIONS = {
  ...Object.fromEntries(OPEN.map((s) => [s.id, s])),
  ...savedSessions(SAVED_COUNT),
};
