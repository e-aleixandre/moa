// schedule-lab-data.js — CATALOG ONLY. Fixtures of the scheduling lab: the
// sessions, the global tasks in the API's shape (so production's rows,
// filters, pinned line and panel draw them), and the scheduled tasks in the
// shape the lab proposes for the API (`when`, `target`, `delivery`, `runs`).

import { zoned, nextOf } from "./schedule-lab-time.js";

// The lab's clock: Wed 30 Sep 2026, 16:40 in Madrid. Scheduled times are
// computed from it so the captures read the same every day; ages of ordinary
// tasks use the real clock like production.
export const LAB_NOW = Date.parse("2026-09-30T14:40:00Z");

export const MOA = "/home/ealeixandre/dev/moa/main";
export const WINERIM = "/home/ealeixandre/dev/winerim-backend/main";
export const INFRA = "/home/ealeixandre/dev/infra";
const now = Date.now();
const min = (n) => now - n * 60000;

const user = (id, text, ago) => ({ msg_id: id, role: "user", timestamp: min(ago), content: [{ type: "text", text }] });
const say = (text, ago = 1) => ({ role: "assistant", timestamp: min(ago), content: [{ type: "text", text }] });
const tool = (id, name, args, status = "done", result = "ok") => ({ _type: "tool_start", tool_call_id: id, tool_name: name, args, status, result });

function session(id, title, cwd, state, ago, extra = {}) {
  return {
    id, title, cwd, state, updated: min(ago),
    model: "Claude Opus 5.5", provider: "anthropic", thinking: "medium",
    permissionMode: "yolo", contextPercent: 31, contextWindow: 200000,
    costUSD: 1.4, runTokensUp: 41000, runTokensDown: 5200,
    messages: [], subagents: {},
    ...(state === "running" ? { runStartedAtMs: min(ago + 2), briefProgress: "Working" } : null),
    ...extra,
  };
}

const CI_MSGS = [
  user("c-u1", "El TestPump_SendPromptGatedWhenQueueNonEmpty falla 1 de cada 20. Arréglalo y abre el MR.", 34),
  say("Lo reproduzco primero con -race y 50 repeticiones.", 33),
  tool("c-b1", "bash", { command: "go test -race -run TestPump_SendPromptGated -count=50 ./pkg/serve/..." }, "done", "--- FAIL: 3/50"),
  tool("c-e1", "edit", { path: "pkg/serve/pump_test.go" }, "done", "ok"),
  tool("c-b2", "bash", { command: "go test -race -run TestPump_SendPromptGated -count=200 ./pkg/serve/..." }, "done", "ok  200/200"),
  tool("c-b3", "bash", { command: "glab mr create --fill && glab ci status" }, "done", "!482 · pipeline running (≈20 min)"),
  tool("c-t1", "tasks", { action: "create", title: "Revisar el CI de !482", when: "in 20m", target: "this session" }, "done", "Scheduled for today 17:00 (Europe/Madrid)"),
  say("El test esperaba a la cola sin sincronizar; ahora espera al evento. 200/200 en local. El MR es !482 y el pipeline tarda unos 20 minutos: **vuelvo a mirarlo a las 17:00**. Si sigue rojo, reintento el job y te aviso.", 2),
];

const DEPLOY_MSGS = [
  user("d-u1", "Prepara la release v0.45: changelog, tag en local y el binario. El despliegue lo programo yo para esta noche.", 300),
  say("Changelog escrito y binario compilado en `dist/moa-v0.45.0`. No he creado el tag remoto.", 290),
  {
    msg_id: "d-ev", role: "user", timestamp: min(1),
    custom: { source: "event", id: "ev_sched_1", source_name: "scheduled", title: "Scheduled task · Desplegar v0.45 en producción · due 03:00, sent 09:14 after your OK", steer: false },
    content: [{ type: "text", text: "moa-update con el binario de main @ 02e5e9e6.\nAntes: release-preflight y comprobar las sesiones activas." }],
  },
  say("Recibido (con 6 h de retraso, confirmado por ti). Paso el preflight antes de nada.", 0),
  tool("d-b1", "bash", { command: "moa sessions --active" }, "running", null),
];

export function labSessions() {
  const list = [
    session("ci", "Arreglar el flake de TestPump", MOA, "idle", 2, { messages: CI_MSGS }),
    session("deploy", "Release v0.45 de moa", MOA, "idle", 290, { messages: DEPLOY_MSGS }),
    session("race", "Carrera en el borrado de adjuntos", MOA, "running", 3, { briefProgress: "go test -race ./pkg/attach" }),
    session("checkout", "Checkout con Stripe", "/home/ealeixandre/dev/ourown-studio/main", "saved", 60 * 26),
    session("tarifas", "Migrar las tarifas de distribuidor", WINERIM, "saved", 60 * 5),
    session("albaranes-30", "Conciliar albaranes · 30 Sep", WINERIM, "running", 40, { briefProgress: "Cruzando 212 líneas" }),
    session("own-infra", "Infra", INFRA, "idle", 60 * 3, { kind: "owner" }),
    session("own-winerim", "Winerim", WINERIM, "saved", 60 * 30, { kind: "owner" }),
    session("own-moa", "moa", MOA, "idle", 20, { kind: "owner" }),
  ];
  return Object.fromEntries(list.map((s) => [s.id, s]));
}

// Where a scheduled task can go: a session, an owner, or a new session in a
// project (with the model it starts with).
export const OWNERS = [
  { id: "own-moa", name: "moa", session_id: "own-moa" },
  { id: "own-infra", name: "Infra", session_id: "own-infra" },
  { id: "own-winerim", name: "Winerim", session_id: "own-winerim" },
];
export const PROJECTS = [
  { key: "moa", cwd: MOA },
  { key: "winerim-backend", cwd: WINERIM },
  { key: "infra", cwd: INFRA },
];

// ── Ordinary tasks (production's shape) ─────────────────────────────────────

let seq = 100;
const rec = (title, place, extra = {}) => ({
  id: ++seq, title, place, status: "pending", description: "", subtasks: [], waits_for: [], unblocks: [],
  created_at: min(extra.ago ?? 60), updated_at: min(extra.ago ?? 60), revision: 1, ...extra,
});

export function labTasks() {
  seq = 100;
  return [
    rec("Aprobar el texto legal del checkout", "you", { requester_session_id: "checkout", project_key: "ourown-studio", project_cwd: "/home/ealeixandre/dev/ourown-studio/main", ago: 180 }),
    rec("Fijar la versión de @playwright/mcp", "you", { project_key: "moa", project_cwd: MOA, ago: 60 * 30 }),
    rec("Volver a emparejar el iPhone", "you", { ago: 60 * 26 }),
    rec("Alarma de disco al 85 %", "backlog", { project_key: "moa", project_cwd: MOA, ago: 60 * 24 }),
    rec("El orden de «más reciente» es falso tras reiniciar", "backlog", { project_key: "moa", project_cwd: MOA, ago: 60 * 96 }),
    rec("Exportar la trazabilidad a PDF por lote", "backlog", { project_key: "winerim-backend", project_cwd: WINERIM, ago: 60 * 48 }),
    rec("Mover la comprobación dentro del lock", "agent", { assignee_session_id: "race", project_key: "moa", project_cwd: MOA, status: "in_progress", ago: 30 }),
    rec("Pasar go vet ./...", "agent", { assignee_session_id: "race", project_key: "moa", project_cwd: MOA, ago: 30 }),
    rec("Renovar el token de Sentry", "you", { requester_session_id: "checkout", status: "done", completed_at: min(300), completion_note: "Nuevo token en 1Password.", ago: 400 }),
  ];
}

// ── Scheduled tasks (the lab's proposal) ─────────────────────────────────────
// when:     { kind:'once', at } | { kind:'repeat', rule }
// target:   { kind:'session', id } | { kind:'owner', id } | { kind:'new', project, model, thinking }
// delivery: { busy:'steer'|'wait', saved:'wake'|'hold', late:'ask'|'run'|'skip' }
// state:    'scheduled' | 'late' (waits for you) | 'failed' | 'paused'
// runs:     newest first; { at, state:'done'|'working'|'late'|'failed'|'skipped', note, child }

export const DEFAULT_DELIVERY = { busy: "steer", saved: "wake", late: "ask" };

const TZ = "Europe/Madrid";
const at = (y, mo, d, h, mi) => zoned(y, mo, d, h, mi, TZ);

export function labScheduled(tz = TZ) {
  const S = (id, title, extra) => ({
    id, title, description: "", subtasks: [], created_at: LAB_NOW - 3600000 * 5, tz,
    delivery: { ...DEFAULT_DELIVERY }, state: "scheduled", runs: [], ...extra,
  });
  const weeklyInfra = { freq: "weekly", dow: 1, h: 8, mi: 0 };
  const albaranes = { freq: "weekdays", h: 7, mi: 30 };
  const ventas = { freq: "weekly", dow: 5, h: 18, mi: 0 };
  return [
    S("s-late", "Rotar el token de Sentry de ourown", {
      when: { kind: "once", at: at(2026, 9, 30, 3, 0) },
      target: { kind: "session", id: "checkout" },
      description: "Crear un token nuevo en Sentry, guardarlo en 1Password («sentry-ourown») y actualizar el secret del deploy.",
      state: "late", late: { due: at(2026, 9, 30, 3, 0), downUntil: at(2026, 9, 30, 9, 12) },
    }),
    S("s-failed", "Pasar go vet tras el refactor", {
      when: { kind: "once", at: at(2026, 9, 30, 12, 0) },
      target: { kind: "session", id: "gone" },
      state: "failed", failure: "the session was deleted",
    }),
    S("s-ci", "Revisar el CI de !482", {
      when: { kind: "once", at: LAB_NOW + 20 * 60000 },
      target: { kind: "session", id: "ci" },
      created_by_session_id: "ci",
      description: "Si sigue rojo, reintentar el job flaky y avisar.",
      created_at: LAB_NOW - 2 * 60000,
    }),
    S("s-deploy", "Desplegar v0.45 en producción", {
      when: { kind: "once", at: at(2026, 10, 1, 3, 0) },
      target: { kind: "session", id: "deploy" },
      description: "moa-update con el binario de main @ 02e5e9e6. Antes: release-preflight y comprobar las sesiones activas.",
      delivery: { busy: "wait", saved: "wake", late: "ask" },
    }),
    S("s-albaranes", "Conciliar albaranes pendientes", {
      when: { kind: "repeat", rule: albaranes },
      target: { kind: "new", project: "winerim-backend", model: "Sonnet 5.5", thinking: "medium" },
      runs: [
        { at: at(2026, 9, 30, 7, 30), state: "working", child: "albaranes-30", note: "Conciliar albaranes · 30 Sep" },
        { at: at(2026, 9, 29, 7, 30), state: "done", note: "3 albaranes cuadrados" },
        { at: at(2026, 9, 28, 7, 30), state: "done", note: "Nada pendiente" },
      ],
    }),
    S("s-infra", "Limpiar el disco: docker builder prune", {
      when: { kind: "repeat", rule: weeklyInfra },
      target: { kind: "owner", id: "own-infra" },
      description: "Si el disco pasa del 80 %, `docker builder prune` y borrar worktrees integrados. Dime cuánto has liberado.",
      runs: [
        { at: at(2026, 9, 28, 8, 0), state: "done", note: "Freed 23 GB" },
        { at: at(2026, 9, 21, 8, 0), state: "done", note: "Freed 6 GB" },
        { at: at(2026, 9, 14, 8, 0), state: "skipped", note: "moa was down; you skipped it" },
        { at: at(2026, 9, 7, 8, 0), state: "failed", note: "the owner was stopped" },
      ],
    }),
    S("s-ventas", "Informe de ventas por distribuidor", {
      when: { kind: "repeat", rule: ventas },
      target: { kind: "owner", id: "own-winerim" },
      state: "paused",
      runs: [{ at: at(2026, 9, 25, 18, 0), state: "done", note: "Enviado a Marta" }],
    }),
  ].map((s) => ({ ...s, next: nextRun(s, tz) }));
}

export function nextRun(s, tz = TZ) {
  if (s.state === "paused") return null;
  if (s.when.kind === "once") return s.when.at;
  return nextOf(s.when.rule, LAB_NOW, tz);
}
