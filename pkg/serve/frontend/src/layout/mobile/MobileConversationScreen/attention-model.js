import { sessionDisplayDotState } from '../../../data/util/format.js';

// These projections drive both the drawer's New results group and the title
// chip. Keeping them here makes their use of the shared dot precedence explicit.
export function aggregateAttention(sessions, activeId) {
  let urgent = 0;
  let unseen = 0;
  let error = 0;
  let permission = 0;
  let arrival = 0;
  for (const session of Object.values(sessions)) {
    if (session.id === activeId) continue;
    const dotState = sessionDisplayDotState(session);
    if (dotState === 'error') {
      urgent += 1;
      error += 1;
      arrival = Math.max(arrival, session.attentionArrival || session.unseenSeq || 0);
    } else if (dotState === 'permission') {
      urgent += 1;
      permission += 1;
      arrival = Math.max(arrival, session.attentionArrival || 0);
    } else if (dotState === 'unseen') {
      unseen += 1;
      arrival = Math.max(arrival, session.attentionArrival || session.unseenSeq || 0);
    }
  }
  return { urgent, unseen, error, permission, arrival };
}

export function newResultSessions(sessions) {
  return sessions.filter((session) => sessionDisplayDotState(session) === 'unseen');
}

export function attentionTone(attention) {
  if (attention.error) return 'error';
  if (attention.permission || attention.urgent) return 'permission';
  if (attention.unseen) return 'unseen';
  return null;
}

export function mobileTitleChipPresentation(attention = {}) {
  const urgent = attention.urgent || 0;
  const unseen = attention.unseen || 0;
  const count = urgent + unseen;
  const tone = attentionTone(attention);
  return {
    count,
    tone,
    hasAttention: count > 0,
    // Aggregate attention carries every active session's occurrence and state.
    // Re-keying the dot on this value restarts its finite animation when a new
    // arrival joins an already-visible indicator, without looping in CSS.
    arrival: attention.arrival || 0,
  };
}

export function mobileTitleChipLabel(title, attention = {}) {
  const { count, hasAttention } = mobileTitleChipPresentation(attention);
  if (!hasAttention) return `${title} — sessions`;
  return `${title} — sessions; ${count} other session${count === 1 ? '' : 's'} need attention`;
}

export function nextMobileTitleRipple(previousArrival, previousRipple, attention) {
  const { arrival } = mobileTitleChipPresentation(attention);
  if (arrival > previousArrival) return { arrival, ripple: previousRipple + 1 };
  return { arrival: previousArrival, ripple: previousRipple };
}
