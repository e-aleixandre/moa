export function opsProjectLabel(cwd) {
  if (!cwd) return 'Project';
  const parts = cwd.split('/').filter(Boolean);
  return parts[parts.length - 1] || cwd;
}

export function sessionStatusLabel(session) {
  const bits = [session.activity || session.lifecycle || 'idle'];
  const jobs = (session.jobs?.subagents || 0) + (session.jobs?.bash || 0);
  if (jobs) bits.push(`${jobs} job${jobs === 1 ? '' : 's'}`);
  if (session.verification && session.verification !== 'unknown') bits.push(`verify ${session.verification}`);
  return bits.join(' · ');
}
