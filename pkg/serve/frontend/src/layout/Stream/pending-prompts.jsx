import { AskUserPrompt, SecretActionPrompt } from "../../components/index.js";

// pendingPromptsTail — what the session is waiting for the owner to answer
// inline, as the stream's tail so it scrolls with the transcript (the owner
// must be able to read earlier context while answering). The ask comes first;
// the secret request follows. Both are keyed by session so a switch remounts,
// and each keys its own card by request id so transcript updates never do.
export function pendingPromptsTail(session) {
  // An asynchronous question does not stop the session, so it does not take
  // the transcript either: it is the compact pin over the composer
  // (AsyncAskPin). Only a blocking question asks inline.
  const ask = !!session.pendingAsk && !session.pendingAsk.async;
  const secret = session.pendingSecrets?.length > 0;
  if (!ask && !secret) return null;
  return (
    <>
      {ask && <AskUserPrompt key={session.id} session={session} />}
      {secret && <SecretActionPrompt key={`secret-${session.id}`} session={session} />}
    </>
  );
}
