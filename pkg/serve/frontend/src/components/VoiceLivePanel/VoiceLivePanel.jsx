import { PhoneOff, Loader2 } from 'lucide-preact';
import './VoiceLivePanel.css';

// VoiceLivePanel — what a live call shows while it runs. Deliberately minimal:
// no captions. Captions invite reading instead of talking, and the transcript
// is a rescue payload, not a display surface. What the owner cannot infer by
// listening is shown instead: whether the call is connected, whether the
// microphone is really live, how many of the five questions are spent, whether
// the delegate is right now waiting for an answer from this conversation, how
// long it has been running, and a way out.

const MIC_COPY = {
  live: { text: 'Mic live', tone: 'ok' },
  'not-live': { text: 'Mic not live — it cannot hear you', tone: 'bad' },
  unknown: { text: 'Mic paused while this tab is in the background', tone: 'warn' },
};

// Closing can take as long as the session needs to finalise (it is what
// carries the final usage), so the copy has to say WHY it is closing. Claiming
// "writing the minutes" while the call is ending because the connection or the
// microphone died would be a comforting lie.
function phaseCopy(phase, endedReason) {
  if (phase === 'connecting') return 'Connecting…';
  if (phase === 'live') return 'On a call';
  if (phase === 'closing') {
    if (endedReason === 'completed') return 'Writing the minutes…';
    // The owner hung up before the delegate wrote anything, so the call is
    // spending a few more seconds asking for the minutes. Saying so is the
    // difference between a wait that makes sense and one that looks stuck.
    if (endedReason === 'hangup') return 'Ending: asking for the minutes…';
    if (endedReason === 'mic-lost') return 'Ending: the microphone was lost…';
    if (endedReason === 'session-error') return 'Ending: the session failed…';
    return 'Ending the call…';
  }
  return 'Call ended';
}

function clock(seconds) {
  const total = Math.max(0, Math.floor(seconds || 0));
  const mins = Math.floor(total / 60);
  const secs = total % 60;
  return `${mins}:${String(secs).padStart(2, '0')}`;
}

// One figure, and what it excludes. Voice minutes are metered by the provider
// and priced at a published rate, so they can be stated. The backend model is
// billed apart, per token, and cannot be priced correctly from here — so it is
// named, not estimated. A number that looked like the cost of the whole call
// would be the one kind of error money display must not make.
function costCopy(cost) {
  if (!cost || typeof cost.voiceUSD !== 'number') return null;
  return {
    text: `$${cost.voiceUSD.toFixed(2)} voice`,
    title: `Voice duration only, billed per second. ${cost.backendModel || 'The backend model'} is billed separately at text rates and is not counted here.`,
  };
}

export function VoiceLivePanel({
  phase, endedReason, micState, questionsUsed, maxQuestions, pendingAsks, elapsed, cost, onHangup,
}) {
  const mic = MIC_COPY[micState] || MIC_COPY.unknown;
  const connecting = phase === 'connecting' || phase === 'closing';
  const spend = costCopy(cost);
  return (
    <div class="voice-live" role="status" aria-live="polite">
      <div class="voice-live-head">
        <span class={`voice-live-dot is-${phase}`} aria-hidden="true" />
        <span class="voice-live-phase">{phaseCopy(phase, endedReason)}</span>
        {connecting && <Loader2 size={14} class="spin" aria-hidden="true" />}
        {phase === 'live' && <span class="voice-live-clock">{clock(elapsed)}</span>}
        <span class="voice-live-spring" />
        {spend && <span class="voice-live-cost" title={spend.title}>{spend.text}</span>}
        <span class="voice-live-questions" title="Questions the delegate may ask this conversation during the call">
          {questionsUsed}/{maxQuestions} questions
        </span>
        <button
          type="button"
          class="voice-live-hangup"
          aria-label="End call"
          title="End call — the minutes land in the composer"
          disabled={phase === 'closing'}
          onClick={onHangup}
        >
          <PhoneOff size={16} aria-hidden="true" />
        </button>
      </div>
      {pendingAsks > 0 && (
        /* Unmistakable on purpose: the delegate is blocked on THIS
           conversation, and the owner is the only one who can unblock it. */
        <div class="voice-live-waiting">
          Waiting for this conversation to answer the delegate…
        </div>
      )}
      <div class={`voice-live-mic is-${mic.tone}`}>{mic.text}</div>
    </div>
  );
}
