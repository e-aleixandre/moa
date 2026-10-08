import { providerWaitDetails } from '../../data/provider-wait.js';

export function ProviderWaitInfo({ execution }) {
  const lines = providerWaitDetails(execution);
  if (execution?.source?.kind === 'api_backup' && ['working', 'awaiting_provider'].includes(execution.phase)) lines.unshift('API backup · cost pending. OAuth remains signed in.');
  if (!lines.length) return null;
  return <div class="provider-wait-info" role="status">{lines.map((line) => <div key={line}>{line}</div>)}</div>;
}
