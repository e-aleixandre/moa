import { useEffect, useMemo } from "preact/hooks";
import { useStore } from "../../hooks/useStore.js";
import { ownersSlice } from "../../data/owners.js";
import { addToast } from "../../data/notifications.js";
import { parsePanelPage, taskDepsPanelPage, taskMovePanelPage, taskPanelPage } from "../../data/session-panel.js";
import { loadTask, loadTaskProjects, patchTask, peekDraft, selectSessionDirectory, setDraftDest, tasksSlice, watchSessionTasks } from "../../data/tasks.js";
import { errorText, movePatch, projectOptions, sessionGroups } from "../../data/tasks-model.js";
import { EmptyState, MoveList, TaskGroups, openTaskSession, useRowChecks, completesInline } from "./parts.jsx";
import { DepsPage, TaskDetail, useTaskLookup } from "./TaskDetail.jsx";

// SessionTasksPage — the session panel's Tasks pages. Tasks lists what this
// session asked of you and its own checklist; a task opens as the next page,
// and on the phone Move is the page after that. Every level is a page of the
// same panel with a real parent (data/session-panel.js), so ‹ and Escape walk
// back one step and a second sheet never opens over the first.
export function SessionTasksPage({ session, page, sheet, goPage }) {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const owners = useStore((st) => ownersSlice(st).list);
  const { kind, id } = parsePanelPage(page);
  const checks = useRowChecks(completesInline);
  const lookup = useTaskLookup();
  useEffect(() => watchSessionTasks(session.id), [session.id]);
  useEffect(() => { if (kind === "taskMove" || kind === "taskNewMove") loadTaskProjects(); }, [kind]);

  const data = slice.bySession[session.id];
  const groups = useMemo(() => sessionGroups(data, session.id), [data, session.id]);


  if (kind === "tasks") {
    return (
      <div class="zl-panel-body is-sub tk-panel-body tk-root">
        {data?.loaded ? (
          <TaskGroups
            groups={groups}
            phone
            context="session"
            sessions={sessions}
            lookup={lookup}
            onSelect={(tid) => goPage(taskPanelPage(tid))}
            completingId={checks.completingId}
            setCompletingId={checks.setCompletingId}
            onCheck={checks.onCheck}
            onAdd={() => goPage("taskNew")}
          />
        ) : <EmptyState compact text="Loading…" />}
      </div>
    );
  }

  if (kind === "taskNew") {
    return (
      <div class="zl-panel-body is-sub tk-panel-detail">
        <TaskDetail
          isNew
          phone={sheet}
          newDest={{ place: "agent", sessionId: session.id }}
          onPushDeps={sheet ? () => goPage("taskNewDeps") : undefined}
          onPushMove={sheet ? () => goPage("taskNewMove") : undefined}
          onCreated={() => goPage("tasks")}
        />
      </div>
    );
  }

  if (kind === "taskDeps" || kind === "taskNewDeps") {
    const back = kind === "taskDeps" ? () => goPage(taskPanelPage(id)) : () => goPage("taskNew");
    return (
      <div class="zl-panel-body is-sub tk-panel-body">
        <DepsPage taskId={kind === "taskDeps" ? id : null} onBack={back} />
      </div>
    );
  }

  if (kind === "taskNewMove") {
    const dest = peekDraft("new")?.dest || { place: "agent", sessionId: session.id };
    return (
      <div class="zl-panel-body is-sub tk-panel-body tk-root">
        <MoveList
          task={{ place: dest.place, project_key: dest.key, assignee_session_id: dest.sessionId }}
          phone
          direct
          projects={projectOptions(slice.projects, slice.list)}
          sessions={sessions}
          owners={owners}
          onPick={(to) => { setDraftDest("new", to); goPage("taskNew"); }}
        />
      </div>
    );
  }

  if (kind === "taskMove") {
    const task = slice.details[id] || null;
    return (
      <div class="zl-panel-body is-sub tk-panel-body tk-root">
        {task && !task.gone ? (
          <MoveList
            task={task}
            phone
            projects={projectOptions(slice.projects, slice.list)}
            sessions={sessions}
            owners={owners}
            onPick={(dest, choice) => {
              patchTask(task.id, movePatch(task, dest, choice))
                .then(() => goPage(taskPanelPage(task.id)))
                .catch((error) => {
                  if (error?.status === 409) loadTask(task.id);
                  addToast({ title: "Could not move the task", detail: errorText(error), type: "error" });
                });
            }}
          />
        ) : <EmptyState compact text="This task is no longer available." />}
      </div>
    );
  }

  return (
    <div class="zl-panel-body is-sub tk-panel-detail">
      <TaskDetail
        key={id}
        taskId={id}
        phone={sheet}
        keys={!sheet}
        onOpenTask={(tid) => goPage(taskPanelPage(tid))}
        onOpenSession={openTaskSession}
        hereSessionId={session.id}
        onPushMove={sheet ? () => goPage(taskMovePanelPage(id)) : undefined}
        onPushDeps={sheet ? () => goPage(taskDepsPanelPage(id)) : undefined}
        onClose={() => goPage("tasks")}
      />
    </div>
  );
}
