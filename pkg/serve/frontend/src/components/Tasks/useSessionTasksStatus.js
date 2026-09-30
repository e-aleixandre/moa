import { useEffect } from "preact/hooks";
import { useStore } from "../../hooks/useStore.js";
import { selectSessionTasks, watchSessionTasks } from "../../data/tasks.js";
import { sessionTasksStatus } from "../../data/tasks-model.js";
import { openTasksView } from "../../data/tasks-view.js";

// useSessionTasksStatus — the status line's Tasks item for one session, the
// same at every density: its words, and the door to the Tasks view narrowed
// to that session.
export function useSessionTasksStatus(sessionId) {
  useEffect(() => (sessionId ? watchSessionTasks(sessionId) : undefined), [sessionId]);
  const data = useStore((state) => (sessionId ? selectSessionTasks(state, sessionId) : null));
  const tasks = sessionTasksStatus(data);
  return {
    tasks,
    onOpenTasks: tasks && sessionId ? () => openTasksView({ sessionId }) : undefined,
  };
}
