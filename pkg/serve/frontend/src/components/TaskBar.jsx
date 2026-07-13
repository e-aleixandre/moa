import { ClipboardList, Map, Shield, Gauge, Zap, Flame, Target, DollarSign } from 'lucide-preact';

/**
 * TaskBar — single-line status bar above the input.
 * Shows: permission mode, extra-usage alert, context usage, plan usage
 * (5h + weekly windows), plan mode, task progress.
 * Always visible when there's anything to show. Compact: never more than one line.
 *
 * `usage` is the global plan usage snapshot from /api/usage (shared across
 * sessions), passed in by the parent.
 */
function usageLevel(pct) {
  return pct >= 80 ? 'usage-high' : pct >= 50 ? 'usage-med' : 'usage-low';
}

// fmtCost mirrors the TUI cost segment: sub-cent spends show 4 decimals so a
// fraction of a cent is still visible, otherwise 2.
function fmtCost(usd) {
  return usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(2)}`;
}

function fmtReset(iso) {
  if (!iso) return '';
  const ms = new Date(iso).getTime() - Date.now();
  if (!(ms > 0)) return 'now';
  const m = Math.floor(ms / 60000);
  const h = Math.floor(m / 60);
  const d = Math.floor(h / 24);
  if (d > 0) return `${d}d ${h % 24}h`;
  if (h > 0) return `${h}h ${m % 60}m`;
  return `${m}m`;
}

function currencySymbol(cur) {
  switch ((cur || '').toUpperCase()) {
    case '':
    case 'USD': return '$';
    case 'EUR': return '€';
    case 'GBP': return '£';
    default: return cur + ' ';
  }
}

// money formats a value the endpoint reports in MINOR currency units (e.g.
// cents) into a display string, scaling by extra.decimal_places (default 2) and
// prefixing extra.currency's symbol.
function money(minor, extra) {
  const dp = extra && Number.isInteger(extra.decimal_places) ? extra.decimal_places : 2;
  const major = (minor ?? 0) / Math.pow(10, dp);
  return `${currencySymbol(extra && extra.currency)}${major.toFixed(2)}`;
}

export function TaskBar({ session, usage }) {
  const tasks = session.tasks || [];
  const planMode = session.planMode;
  const hasPlan = planMode && planMode !== 'off';
  const goalActive = !!session.goalActive;
  const hasTasks = tasks.length > 0;
  const contextPct = session.contextPercent ?? -1;
  const permMode = session.permissionMode || 'yolo';
  const hasContext = contextPct >= 0;
  const costUSD = session.costUSD ?? 0;
  const hasCost = costUSD > 0;

  const u = usage && usage.available ? usage : null;
  const isAnthropic = !session.provider || session.provider === 'anthropic';
  const fiveH = isAnthropic && u && u.five_hour;
  const week = isAnthropic && u && u.seven_day;
  const extra = isAnthropic && u && u.extra_usage;
  const showExtra = extra && extra.is_enabled;
  const extraOn = showExtra && (extra.used_credits ?? 0) > 0;
  const hasUsage = !!(fiveH || week || showExtra);
  const onOverage = !!session.onOverage;

  if (!hasPlan && !goalActive && !hasTasks && !hasContext && !hasUsage && !onOverage && !hasCost) return null;

  const done = tasks.filter(t => t.status === 'done').length;
  const total = tasks.length;
  const current = tasks.find(t => t.status !== 'done');

  const contextClass = contextPct >= 80 ? 'ctx-high' : contextPct >= 50 ? 'ctx-med' : 'ctx-low';

  const extraTitle = showExtra
    ? `Extra usage ON — ${money(extra.used_credits, extra)} used`
      + (extra.monthly_limit != null ? ` of ${money(extra.monthly_limit, extra)}` : '')
    : '';

  return (
    <div class="task-bar">
      <span class={`task-bar-pill perm-${permMode}`} title={`Permission mode: ${permMode}`}>
        <Shield />
        {permMode.toUpperCase()}
      </span>

      {onOverage && (
        <span class="task-bar-pill session-overage" title="Esta sesión se está sirviendo desde extra usage (pago por uso)">
          <Flame />
          en extra
        </span>
      )}

      {showExtra && (
        <span class={`task-bar-pill usage-extra ${extraOn ? 'on' : ''}`} title={extraTitle}>
          <Zap />
          {money(extra.used_credits, extra)}
          {extra.monthly_limit != null && `/${money(extra.monthly_limit, extra)}`}
        </span>
      )}

      {hasContext && (
        <span class={`task-bar-pill ${contextClass}`} title={`Context: ${contextPct}%`}>
          <Gauge />
          {contextPct}%
        </span>
      )}

      {hasCost && (
        <span class="task-bar-pill cost" title={`Session cost (main run + subagents): ${fmtCost(costUSD)}`}>
          <DollarSign />
          {fmtCost(costUSD)}
        </span>
      )}

      {fiveH && (
        <span
          class={`task-bar-pill ${usageLevel(fiveH.utilization)}`}
          title={`Session (5h): ${Math.round(fiveH.utilization)}% · resets in ${fmtReset(fiveH.resets_at)}`}
        >
          5h {Math.round(fiveH.utilization)}%
        </span>
      )}

      {week && (
        <span
          class={`task-bar-pill ${usageLevel(week.utilization)}`}
          title={`Week: ${Math.round(week.utilization)}% · resets in ${fmtReset(week.resets_at)}`}
        >
          wk {Math.round(week.utilization)}%
        </span>
      )}

      {hasPlan && (
        <span class={`task-bar-pill plan-${planMode}`}>
          <Map />
          {planMode}
        </span>
      )}

      {goalActive && (
        <span class="task-bar-pill goal" title={session.goalObjective || 'Goal active'}>
          <Target />
          {session.goalVerifying
            ? 'goal · verifying…'
            : `goal${session.goalIteration ? ` ${session.goalIteration}` : ''}`}
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
