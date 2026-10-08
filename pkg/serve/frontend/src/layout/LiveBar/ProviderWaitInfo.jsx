import { providerWaitDetails } from '../../data/provider-wait.js';

export function ProviderWaitInfo({ execution }) {
  const lines = providerWaitDetails(execution);
  if (!lines.length) return null;
  return <div class="provider-wait-info" role="status">{lines.map((line) => <div key={line}>{line}</div>)}</div>;
}
