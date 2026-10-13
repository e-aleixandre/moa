import { SecretActionCard } from "./SecretActionCard.jsx";
import { resolveSecretAction } from "../../data/session-actions.js";
import { pendingSecrets } from "../../data/secret-action.js";

// SecretActionPrompt — the container: one card at a time, the oldest request
// first, so several subagents asking at once are answered in order and the
// strip never grows. Keyed by request id, so a request never inherits another
// one's typed values.
export function SecretActionPrompt({ session }) {
  const pending = pendingSecrets(session);
  const request = pending[0];
  if (!request) return null;
  return (
    <SecretActionCard
      key={request.id}
      session={session}
      request={request}
      position={1}
      total={pending.length}
      send={(id, body) => resolveSecretAction(session.id, id, body)}
    />
  );
}
