import { PhoneOff, Loader2 } from 'lucide-preact';
import './VoiceLivePanel.css';

// VoiceLivePanel — what a live call shows while it runs. Deliberately minimal:
// no captions. Captions invite reading instead of talking, and the transcript
// is a rescue payload, not a display surface. What the owner cannot infer by
// listening is shown instead: whether the call is connected, whether the
// microphone is really live, how many of the five questions are spent, whether
// the delegate is right now waiting for an answer from this conversation, how
// long it has been running, and a way out.
//
// ONE FLAT ROW, not a card. It used to be a filled, rimmed block inside the
// composer's slab — a plane inside a plane, three lines tall in its normal
// state, pushing the box the owner types in down the screen. A call in its
// ordinary state is one line now:
//
//   ● On a call   1:24        $0.42  2/5  ⌫
//
// and only the two things that need the owner earn a line of their own: the
// delegate blocked on THIS conversation (amber, unmistakable), and a
// microphone that is not live. A healthy mic says nothing — the call being on
// screen already says it is on.
//
// The input stays reachable while a call runs: the delegate can block waiting
// for an answer from this conversation, and the answer is typed here.

const MIC_COPY = {
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

// callCost reads the spend from either shape the branches use: `costUSD` here,
// and `{ voiceUSD, backendModel }` on feat/voice-delegate, where the figure is
// real. Accepting both is three lines and it removes the one way this merge
// could silently go wrong: resolving it in this branch's favour and dropping
// the number the owner asked to see. The title still names what is NOT counted
// when the branch that knows the backend model says so.
function callCost({ costUSD, cost }) {
  const usd = typeof cost?.voiceUSD === 'number' ? cost.voiceUSD : costUSD;
  if (!(usd > 0)) return null;
  return {
    text: `$${usd.toFixed(2)}`,
    title: cost?.backendModel
      ? `Voice duration only, billed per second. ${cost.backendModel} is billed separately at text rates and is not counted here.`
      : 'What this call has cost so far (voice model)',
  };
}

export function VoiceLivePanel({
  phase, endedReason, micState, questionsUsed, maxQuestions, pendingAsks, elapsed, onHangup,
  // The spend so far. Drawn whenever it arrives and absent when it does not, so
  // a branch whose hook does not report it keeps the same row.
  costUSD, cost,
}) {
  const spend = callCost({ costUSD, cost });
  // A healthy mic is silent: MIC_COPY has no `live` entry on purpose.
  const mic = micState && micState !== 'live' ? (MIC_COPY[micState] || MIC_COPY.unknown) : null;
  const connecting = phase === 'connecting' || phase === 'closing';
  return (
    <div class="voice-live" role="status" aria-live="polite">
      <div class="voice-live-head">
        <span class={`voice-live-dot is-${phase}`} aria-hidden="true" />
        <span class="voice-live-phase">{phaseCopy(phase, endedReason)}</span>
        {connecting && <Loader2 size={14} class="spin" aria-hidden="true" />}
        {phase === 'live' && <span class="voice-live-clock">{clock(elapsed)}</span>}
        <span class="voice-live-spring" />
        {spend && (
          <span class="voice-live-cost" title={spend.title}>{spend.text}</span>
        )}
        <span class="voice-live-questions" title="Questions the delegate may ask this conversation during the call">
          {questionsUsed}/{maxQuestions}
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
      {mic && <div class={`voice-live-mic is-${mic.tone}`}>{mic.text}</div>}
    </div>
  );
}
