import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { Plus } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { hasBlockingOverlay } from "../../data/overlays.js";
import { openSession } from "../../data/tile-actions.js";
import { addToast } from "../../data/notifications.js";
import {
  loadTaskProjects, markTasksSeen, patchTask, selectSessionDirectory, setTasksAgents, tasksSlice,
} from "../../data/tasks.js";
import {
  errorText, flatRows, groupTasks, isTypingTarget, movePatch, nextSelection, openOwnCount, projectOptions,
} from "../../data/tasks-model.js";
import { CloseIcon, EmptyState, Filters, Keycap, TaskGroups, completesInline, useRowChecks } from "./parts.jsx";
import { TaskDetail, taskEyebrow, useTaskLookup } from "./TaskDetail.jsx";

// TasksScreen — the global view, in the desktop's middle zone. You (requests
// above notes), a Backlog per project, the agents' checklists behind their
// filter, and Done folded. The open task docks in the third zone, where a
// session's dossier would be. C creates, j/k and the arrows walk the rows,
// M moves and ⌘↵ completes the open task, Escape closes it.
export function TasksScreen() {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const [sel, setSel] = useState(null);
  const [creating, setCreating] = useState(false);
  const [project, setProject] = useState(null);
  const [doneOpen, setDoneOpen] = useState(false);
  const [dropInit, setDropInit] = useState(null);
  const checks = useRowChecks(completesInline);
  const listRef = useRef(null);
  const lookup = useTaskLookup();

  useEffect(() => { markTasksSeen(); loadTaskProjects(); }, []);

  const groups = useMemo(
    () => groupTasks(slice.list, { agents: slice.agents, project, sessions }),
    [slice.list, slice.agents, project, sessions],
  );
  const rows = flatRows(groups, doneOpen);
  const projects = projectOptions(slice.projects, slice.list);
  const task = sel ? slice.list.find((t) => t.id === sel) || slice.details[sel] || null : null;

  const open = (id) => { setSel(id); setCreating(false); setDropInit(null); };
  const nav = useRef({});
  nav.current = { rows, sel };
  useEffect(() => {
    const onKey = (e) => {
      if (isTypingTarget(e.target) || hasBlockingOverlay() || e.defaultPrevented) return;
      if (e.metaKey || e.altKey || e.ctrlKey) return;
      const { rows: r, sel: s } = nav.current;
      if (e.key === "ArrowDown" || e.key === "j") { e.preventDefault(); open(nextSelection(r, s, 1)); }
      else if (e.key === "ArrowUp" || e.key === "k") { e.preventDefault(); open(nextSelection(r, s, -1)); }
      else if (e.key === "c" || e.key === "C") { e.preventDefault(); setCreating(true); setSel(null); }
      else if (e.key === "Escape") { setSel(null); setCreating(false); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    listRef.current?.querySelector(`[data-task="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  // Dropping on a group moves; dropping on an agent's group opens Move with
  // that session picked, because assigning tells the session and asks first.
  const onDropTask = (id, dest) => {
    const t = slice.list.find((x) => Number(x.id) === Number(id));
    if (!t) return;
    if (dest.place === "agent") { setSel(t.id); setCreating(false); setDropInit({ menu: true, pending: dest.sessionId }); return; }
    patchTask(t.id, movePatch(t, dest)).catch((error) => addToast({ title: "Could not move the task", detail: errorText(error), type: "error" }));
  };

  const count = openOwnCount(slice.list);
  const hasOpen = groups.some((g) => g.id !== "done" && g.rows.length);
  const hasDone = groups.some((g) => g.id === "done");

  return (
    <>
      <main class="conversation-main tk-main tk-root" aria-label="Tasks">
        <header class="zl-desk-head tk-head">
          <div class="tk-crumb">
            <span class="zl-crumb-title">Tasks</span>
            {count > 0 && <span class="tk-crumb-n zl-data">{count} open</span>}
          </div>
          <span class="tk-grow" />
          <button type="button" class="zl-ask-btn tk-newbtn" onClick={() => { setCreating(true); setSel(null); }}>
            <Plus size={15} aria-hidden="true" />New task<Keycap>C</Keycap>
          </button>
        </header>
        <Filters agents={slice.agents} setAgents={setTasksAgents} project={project} setProject={setProject} projects={projects} />
        <div class="tk-scroll" ref={listRef}>
          {slice.error && !slice.loaded && <p class="tk-banner" role="status">Couldn't load tasks: {slice.error}</p>}
          {slice.loaded && !hasOpen && <EmptyState compact={hasDone} onNew={() => setCreating(true)} />}
          {(hasOpen || hasDone) && (
            <TaskGroups
              groups={groups}
              selected={sel}
              onSelect={open}
              sessions={sessions}
              lookup={lookup}
              newSince={slice.newSince}
              completingId={checks.completingId}
              setCompletingId={checks.setCompletingId}
              onCheck={checks.onCheck}
              onDropTask={onDropTask}
              doneOpen={doneOpen}
              setDoneOpen={setDoneOpen}
              draggable
            />
          )}
        </div>
      </main>
      {(creating || task) && (
        <div class="desktop-dossier is-open tk-dossier">
          <aside class="zl-side zl-side-right is-open" role="dialog" aria-label={creating ? "New task" : task.title}>
            <div class="zl-side-head">
              <span class="zl-side-title is-eyebrow">{creating ? "New task" : taskEyebrow(task)}</span>
              <button type="button" class="zl-x" onClick={() => { setSel(null); setCreating(false); }} aria-label="Close"><CloseIcon /></button>
            </div>
            {creating ? (
              <TaskDetail isNew key="new" onCreated={(id) => { setCreating(false); if (id) setSel(id); }} />
            ) : (
              <TaskDetail
                key={task.id}
                taskId={task.id}
                keys
                init={dropInit || {}}
                onOpenTask={open}
                onOpenSession={(id) => openSession(id)}
                onClose={() => setSel(null)}
              />
            )}
          </aside>
        </div>
      )}
    </>
  );
}
