import { useEffect } from "preact/hooks";
import { useStore } from "../../hooks/useStore.js";
import { listMark, selectSessionTasks, tasksSlice, watchSessionTasks } from "../../data/tasks.js";
import { sessionTasksStatus } from "../../data/tasks-model.js";
import { openTasksView } from "../../data/tasks-view.js";

// useSessionTasksStatus — the status line's Tasks item for one session, the
// same at every density: its words, and the door to the Tasks view narrowed
// to that session.
export function useSessionTasksStatus(sessionId) {
  useEffect(() => (sessionId ? watchSessionTasks(sessionId) : undefined), [sessionId]);
  const data = useStore((state) => (sessionId ? selectSessionTasks(state, sessionId) : null));
  const marks = useStore((state) => tasksSlice(state).marks);
  const tasks = sessionTasksStatus(data, Date.now(), undefined, sessionId ? listMark(marks, sessionId) : Infinity);
  return {
    tasks,
    onOpenTasks: tasks && sessionId ? () => openTasksView({ sessionId }) : undefined,
  };
}
