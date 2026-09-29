import { useEffect, useMemo, useState } from "preact/hooks";
import { Plus } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { useEdgeSwipeBack } from "../../hooks/useEdgeSwipeBack.js";
import { addToast } from "../../data/notifications.js";
import { openSession } from "../../data/tile-actions.js";
import {
  loadTask, loadTaskProjects, markTasksSeen, patchTask, selectSessionDirectory, setTasksAgents, tasksSlice,
} from "../../data/tasks.js";
import { errorText, groupTasks, movePatch, projectOptions } from "../../data/tasks-model.js";
import { BackIcon, EmptyState, Filters, MoveList, TaskGroups, useRowChecks, completesInline } from "./parts.jsx";
import { TaskDetail, useTaskLookup } from "./TaskDetail.jsx";
import "../../layout/mobile/MobileConversationScreen/MobileInboxView.css";

// MobileTasksView — the Tasks screen on the phone: a full-screen push, like
// the inbox. A task and its Move are PAGES of this same screen, each with ‹
// back to the one before (decisions: one sheet at a time on the phone), and
// the edge swipe walks back the same way.
export function MobileTasksView({ onBack }) {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const [stack, setStack] = useState([]);
  const [project, setProject] = useState(null);
  const [doneOpen, setDoneOpen] = useState(false);
  const checks = useRowChecks(completesInline);
  const lookup = useTaskLookup();
  useEffect(() => { markTasksSeen(); loadTaskProjects(); }, []);

  const top = stack[stack.length - 1] || null;
  const push = (p) => setStack([...stack, p]);
  const pop = () => setStack(stack.slice(0, -1));
  const back = top ? pop : onBack;
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack: back });

  const groups = useMemo(
    () => groupTasks(slice.list, { agents: slice.agents, project, sessions }),
    [slice.list, slice.agents, project, sessions],
  );
  const projects = projectOptions(slice.projects, slice.list);
  const hasOpen = groups.some((g) => g.id !== "done" && g.rows.length);
  const hasDone = groups.some((g) => g.id === "done");

  let title = "Tasks";
  let body;
  if (!top) {
    body = (
      <div class="tk-phone-body">
        <Filters phone agents={slice.agents} setAgents={setTasksAgents} project={project} setProject={setProject} projects={projects} />
        {slice.loaded && !hasOpen && <EmptyState phone compact={hasDone} onNew={() => push({ kind: "new" })} />}
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
    body = <TaskDetail isNew phone onCreated={(id) => setStack(id ? [{ kind: "task", id }] : [])} />;
  } else if (top.kind === "move") {
    title = "Move to";
    const task = slice.details[top.id];
    body = (
      <div class="tk-phone-body">
        {task && !task.gone && (
          <MoveList
            task={task}
            phone
            projects={projects}
            sessions={sessions}
            onPick={(dest, choice) => {
              patchTask(task.id, movePatch(task, dest, choice))
                .then(pop)
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
        onOpenSession={(id) => { if (openSession(id)) onBack(); }}
        onPushMove={() => push({ kind: "move", id: top.id })}
        onClose={pop}
      />
    );
  }

  return (
    <div class={dragging ? "minbox is-swiping" : "minbox"} ref={screenRef} {...swipeBind}>
      <div class="zi-inbox is-phone tk-push-in tk-root">
        <div class="zi-head is-sheet">
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
