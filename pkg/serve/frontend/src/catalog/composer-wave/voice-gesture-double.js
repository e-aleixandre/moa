import { useContext } from "preact/hooks";
import { useVoiceGesture as useRealVoiceGesture } from "../../hooks/useVoiceGesture.js";
import { LabVoice } from "./voice-context.js";

const inert = { onPointerDown() {}, onClick() {} };

// Lab double (see catalog-serve.mjs): the Composer's dictation, pinned to
// "recording" when ?view=composer-wave asks for it. The mic button is inert
// there so a click cannot open a real recorder behind the specimen.
export function useVoiceGesture(opts) {
  const real = useRealVoiceGesture(opts);
  const lab = useContext(LabVoice);
  if (!lab) return real;
  return { ...real, handlers: inert, recording: !!lab.recording, transcribing: false, supported: true };
}
