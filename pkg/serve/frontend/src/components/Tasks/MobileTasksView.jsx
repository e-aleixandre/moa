import { useEffect, useMemo, useState } from "preact/hooks";
import { Plus } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { useEdgeSwipeBack } from "../../hooks/useEdgeSwipeBack.js";
import { addToast } from "../../data/notifications.js";
import { ownersSlice } from "../../data/owners.js";
import {
  loadTask, loadTaskProjects, markTasksSeen, patchTask, peekDraft, selectSessionDirectory, setDraftDest, setTasksAgents, setTasksSession, tasksSlice, watchSessionTasks,
} from "../../data/tasks.js";
import { errorText, groupTasks, movePatch, projectOptions, sessionGroups, sessionName } from "../../data/tasks-model.js";
import { projectLabels } from "../../data/tasks-move.js";
import { BackIcon, EmptyState, Filters, MoveList, TaskGroups, openTaskSession, useEscape, useRowChecks, completesInline } from "./parts.jsx";
import { DepsPage, TaskDetail, useTaskLookup } from "./TaskDetail.jsx";
import { ReroutePage, ScheduledGroup, TargetPage, WhenPage } from "./Scheduled.jsx";
import { scheduledRows } from "../../data/schedule-model.js";
import "../../layout/mobile/MobileConversationScreen/MobileInboxView.css";

// MobileTasksView — the Tasks screen on the phone: a full-screen push, like
// the inbox. A task and its Move are PAGES of this same screen, each with ‹
// back to the one before (decisions: one sheet at a time on the phone), and
// the edge swipe walks back the same way.
export function MobileTasksView({ onBack }) {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const owners = useStore((st) => ownersSlice(st).list);
  const [stack, setStack] = useState([]);
  const [project, setProject] = useState(null);
  const [doneOpen, setDoneOpen] = useState(false);
  const checks = useRowChecks(completesInline);
  const lookup = useTaskLookup();
  useEffect(() => { markTasksSeen(); loadTaskProjects(); }, []);

  const top = stack[stack.length - 1] || null;
  useEffect(() => { if (top?.kind === "move") loadTaskProjects(); }, [top?.kind]);
  const push = (p) => setStack([...stack, p]);
  const pop = () => setStack(stack.slice(0, -1));
  const back = top ? pop : onBack;
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack: back });
  // Escape walks back one page, like ‹ (a decision line on a page answers
  // first: it is newer on the escape stack).
  useEscape(!!top, pop);

  // Narrowed to a session (its status line opened the view): its own tasks.
  const only = slice.session;
  useEffect(() => (only ? watchSessionTasks(only) : undefined), [only]);
  const onlyData = only ? slice.bySession[only] : null;
  const groups = useMemo(
    () => (only ? sessionGroups(onlyData, only).map((g) => ({ ...g, add: false })) : groupTasks(slice.list, { agents: slice.agents, project, sessions })),
    [only, onlyData, slice.list, slice.agents, project, sessions],
  );
  const projects = projectOptions(slice.projects, slice.list);
  const scheduled = useMemo(
    () => scheduledRows(only ? onlyData?.scheduled : slice.list.filter((t) => !project || t.project_key === project)),
    [only, onlyData, slice.list, project],
  );
  const hasOpen = scheduled.length > 0 || groups.some((g) => g.id !== "done" && g.rows.length);
  // A scheduled task's When, Send to and reroute are pages of this screen
  // too; its draft waits in the stash while they show.
  const schedPages = (key) => ({
    onPushWhen: () => push({ kind: "when", key }),
    onPushTarget: () => push({ kind: "target", key }),
    onPushReroute: () => push({ kind: "reroute", id: key }),
  });
  const hasDone = groups.some((g) => g.id === "done");

  let title = "Tasks";
  let body;
  if (!top) {
    body = (
      <div class="tk-phone-body">
        <Filters
          phone
          agents={slice.agents}
          setAgents={setTasksAgents}
          project={project}
          setProject={setProject}
          projects={projects}
          session={only ? sessionName(sessions, only) : null}
          onClearSession={() => setTasksSession(null)}
        />
        {(only ? !!onlyData?.loaded : slice.loaded) && !hasOpen && <EmptyState phone compact={hasDone} onNew={() => push({ kind: "new" })} />}
        <ScheduledGroup list={scheduled} phone limit={4} onSelect={(id) => push({ kind: "task", id })} />
        {(hasOpen || hasDone) && (
          <TaskGroups
            groups={groups}
            phone
            sessions={sessions}
            lookup={lookup}
            newSince={slice.newSince}
            onSelect={(id) => push({ kind: "task", id })}
            completingId={checks.completingId}
            setCompletingId={checks.setCompletingId}
            onCheck={checks.onCheck}
            doneOpen={doneOpen}
            setDoneOpen={setDoneOpen}
          />
        )}
      </div>
    );
  } else if (top.kind === "new") {
    title = "New task";
    body = (
      <TaskDetail
        isNew
        phone
        newDest={only ? { place: "agent", sessionId: only } : null}
        onPushDeps={() => push({ kind: "deps", id: null })}
        onPushMove={() => push({ kind: "move", id: null })}
        {...schedPages("new")}
        onOpenSession={openTaskSession}
        onCreated={(id) => setStack(id ? [{ kind: "task", id }] : [])}
      />
    );
  } else if (top.kind === "when") {
    title = "When";
    body = <WhenPage draftKey={top.key} onBack={pop} />;
  } else if (top.kind === "target") {
    title = top.level ? projectLabels(projects).get(top.level) || "Project" : "Send to";
    const done = () => setStack(stack.filter((p) => p.kind !== "target"));
    body = (
      <TargetPage
        key={top.level || "top"}
        draftKey={top.key}
        level={top.level || null}
        onLevel={(level) => (level ? push({ kind: "target", key: top.key, level }) : pop())}
        onDone={done}
      />
    );
  } else if (top.kind === "reroute") {
    title = "Send to";
    body = <ReroutePage taskId={top.id} onDone={pop} />;
  } else if (top.kind === "deps") {
    title = "Waits for";
    body = <DepsPage taskId={top.id} onBack={pop} />;
  } else if (top.kind === "move") {
    // Move is a page, and a project opened inside it is the next page, so ‹
    // walks back one level at a time. A task still being written only
    // chooses where it will go: its draft waits in the stash.
    const isNew = top.id == null;
    const dest = isNew ? peekDraft("new")?.dest || { place: "you" } : null;
    const task = isNew
      ? { place: dest.place, project_key: dest.key, assignee_session_id: dest.sessionId }
      : slice.details[top.id];
    title = top.level ? projectLabels(projects).get(top.level) || "Project" : "Move to";
    const done = () => setStack(stack.filter((p) => p.kind !== "move"));
    body = (
      <div class="tk-phone-body">
        {task && !task.gone && (
          <MoveList
            key={top.level || "top"}
            task={task}
            phone
            direct={isNew}
            projects={projects}
            sessions={sessions}
            owners={owners}
            level={top.level || null}
            onLevel={(level) => (level ? push({ kind: "move", id: top.id, level }) : pop())}
            onPick={(to, choice) => {
              if (isNew) { setDraftDest("new", to); done(); return; }
              patchTask(task.id, movePatch(task, to, choice))
                .then(done)
                .catch((error) => {
                  if (error?.status === 409) loadTask(task.id);
                  addToast({ title: "Could not move the task", detail: errorText(error), type: "error" });
                });
            }}
          />
        )}
      </div>
    );
  } else {
    title = "";
    body = (
      <TaskDetail
        key={top.id}
        taskId={top.id}
        phone
        onOpenTask={(id) => push({ kind: "task", id })}
        onOpenSession={openTaskSession}
        onPushMove={() => push({ kind: "move", id: top.id })}
        onPushDeps={() => push({ kind: "deps", id: top.id })}
        {...schedPages(top.id)}
        onClose={pop}
      />
    );
  }

  return (
    <div class={dragging ? "minbox is-swiping" : "minbox"} ref={screenRef} {...swipeBind}>
      <div class="zi-inbox is-phone tk-push-in tk-root">
        <div class="zi-head">
          <button type="button" class="zi-back" onClick={back} aria-label={top ? "Back" : "Back to the conversation"}><BackIcon /></button>
          <span class="zi-title">{title}</span>
          {!top && (
            <button type="button" class="zi-x tk-plus" aria-label="New task" onClick={() => push({ kind: "new" })}>
              <Plus size={18} aria-hidden="true" />
            </button>
          )}
        </div>
        <div class="tk-page" key={stack.length}>{body}</div>
      </div>
    </div>
  );
}
