export function newerProviderExecution(previous, next) {
  if (!next || typeof next !== 'object') return previous || null;
  if (!previous || next.generation > previous.generation
    || (next.generation === previous.generation && next.epoch >= previous.epoch)) return next;
  return previous;
}

export function providerWaitDetails(execution) {
  if (execution?.phase !== 'provider_wait') return [];
  const wait = execution.wait || {};
  const lines = [];
  const stamp = (value) => {
    const time = Date.parse(value);
    return Number.isFinite(time) ? new Date(time).toLocaleString() : '';
  };
  if (wait.reset_at && stamp(wait.reset_at)) lines.push(`Announced reset: ${stamp(wait.reset_at)}`);
  if (wait.next_attempt_at && stamp(wait.next_attempt_at)) lines.push(`Next attempt: ${stamp(wait.next_attempt_at)}`);
  if (execution.save_error) lines.push(execution.save_error);
  lines.push(execution.bound
    ? 'To use the selected model, Stop, then continue.'
    : 'Change model to try now, or Stop to end the wait.');
  return lines;
}
