import { ClipboardList, Map, Shield, Gauge } from 'lucide-preact';

/**
 * TaskBar — single-line status bar above the input.
 * Shows: permission mode, context usage, plan mode, task progress.
 * Always visible when there's anything to show. Compact: never more than one line.
 */
export function TaskBar({ session }) {
  const tasks = session.tasks || [];
  const planMode = session.planMode;
  const hasPlan = planMode && planMode !== 'off';
  const hasTasks = tasks.length > 0;
  const contextPct = session.contextPercent ?? -1;
  const permMode = session.permissionMode || 'yolo';
  const hasContext = contextPct >= 0;

  if (!hasPlan && !hasTasks && !hasContext) return null;

  const done = tasks.filter(t => t.status === 'done').length;
  const total = tasks.length;
  const current = tasks.find(t => t.status !== 'done');

  const contextClass = contextPct >= 80 ? 'ctx-high' : contextPct >= 50 ? 'ctx-med' : 'ctx-low';

  return (
    <div class="task-bar">
      <span class={`task-bar-pill perm-${permMode}`} title={`Permission mode: ${permMode}`}>
        <Shield />
        {permMode.toUpperCase()}
      </span>

      {hasContext && (
        <span class={`task-bar-pill ${contextClass}`} title={`Context: ${contextPct}%`}>
          <Gauge />
          {contextPct}%
        </span>
      )}

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
