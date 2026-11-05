import { Mic, Loader2 } from "lucide-preact";

// MicButton — the dictation button, one face everywhere it appears. The
// composer's own look (zl-attach zl-mic: the peach fill and halo while it
// records, the spinner while it transcribes) lives in Composer.css; this
// component only binds the useVoiceGesture state to it, so a second place that
// dictates cannot drift into its own mic.
export function MicButton({ voice, disabled = false, idleTitle = "Dictate" }) {
  const { recording, transcribing, handlers } = voice;
  return (
    <button
      type="button"
      class={`zl-attach zl-mic${recording ? " recording" : ""}${transcribing ? " transcribing" : ""}`}
      aria-label={transcribing ? "Transcribing" : recording ? "Stop recording" : "Dictate"}
      title={
        transcribing ? "Transcribing…"
          : recording ? "Tap to stop & transcribe · Esc discards"
            : idleTitle
      }
      disabled={transcribing || disabled}
      {...handlers}
    >
      {/* Recording keeps the mic glyph: a live microphone is a state of MY
          input, not a stop control. The ring says "live". */}
      {transcribing ? <Loader2 size={15} class="spin" /> : <Mic size={15} />}
    </button>
  );
}
