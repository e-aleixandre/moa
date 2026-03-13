import { ClipboardList, Map } from 'lucide-preact';

/**
 * TaskBar — single-line task progress + plan mode indicator above the input.
 * Compact: never more than one line. Use /tasks to see the full list.
 */
export function TaskBar({ session }) {
  const tasks = session.tasks || [];
  const planMode = session.planMode;
  const hasPlan = planMode && planMode !== 'off';
  const hasTasks = tasks.length > 0;

  if (!hasPlan && !hasTasks) return null;

  const done = tasks.filter(t => t.status === 'done').length;
  const total = tasks.length;

  // Find first pending task for "current" display.
  const current = tasks.find(t => t.status !== 'done');

  return (
    <div class="task-bar">
      {hasPlan && (
        <span class={`task-bar-pill plan-${planMode}`}>
          <Map />
          {planMode}
        </span>
      )}
      {hasTasks && (
        <span class="task-bar-pill tasks">
          <ClipboardList />
          {done}/{total}
          {done === total && total > 0 && ' ✓'}
        </span>
      )}
      {current && (
        <span class="task-bar-current" title={current.title}>
          → {current.title}
        </span>
      )}
    </div>
  );
}
