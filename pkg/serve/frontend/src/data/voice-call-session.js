// voice-call-session.js — the one voice call of this client, in the app store.
//
// A call belongs to the app, not to a composer: the owner row can start it
// without opening the conversation, the phone's call screen and the
// conversation's composer both show it, and switching conversations does not
// hang up. The store slice `voiceCall` mirrors the controller's state plus
// which session it is with and whether the phone's call screen is open.

import { store, setState } from './store.js';
import { VoiceCallController } from './voice-call.js';
import { createCallAudio } from './voice-call-audio.js';
import { claimWakeLock } from './wake-lock.js';
import { nativeCallBridge, createNativeCallDeps } from './voice-call-native.js';
import { sessionTitle } from './util/format.js';

let controller = null;
let callName = '';
let callSession = '';

// The name the phone shows for the call: the owner, or the conversation.
function partyName(sessionId) {
  const s = store.get();
  const owner = (s.owners?.list || []).find((o) => o.session_id === sessionId);
  const session = s.sessions?.[sessionId];
  return owner?.name || (session ? sessionTitle(session) : '');
}

// Injectable for tests: the controller's I/O boundary. Inside the iOS app
// the socket and audio are the app's own (a system call that survives the
// lock screen); elsewhere they are the page's.
// Off until the iOS app's native call is proven on a device: build 7 closes
// its socket right after it opens, so every call goes through the page.
const NATIVE_CALLS = false;
const nativeBridge = NATIVE_CALLS ? nativeCallBridge() : null;
const webDeps = { fetch: undefined, WebSocket: undefined, createAudio: () => createCallAudio({ claimWakeLock }) };
const webFallback = { WebSocket: globalThis.WebSocket, createAudio: webDeps.createAudio };
const browserDeps = nativeBridge
  ? { fetch: undefined, ...createNativeCallDeps(nativeBridge, { name: () => callName, sessionId: () => callSession }), fallback: webFallback }
  : webDeps;

// A native call that dropped before it had audio is taken over by the page;
// later calls on this page go straight to the page's own transport.
function nativeFailed() {
  if (deps === browserDeps) deps = webDeps;
}
let deps = browserDeps;
export function __setVoiceCallDepsForTests(next) {
  controller = null;
  deps = next ? { ...browserDeps, ...next } : browserDeps;
  setState({ voiceCall: null });
}

export function voiceCallSupported() {
  if (typeof deps.supported === 'boolean') return deps.supported;
  return typeof globalThis.WebSocket === 'function'
    && typeof (globalThis.AudioContext || globalThis.webkitAudioContext) === 'function'
    && typeof globalThis.AudioWorkletNode === 'function'
    && !!globalThis.navigator?.mediaDevices?.getUserMedia;
}

function publish(sessionId, state) {
  const prev = store.get().voiceCall;
  setState({ voiceCall: { ...state, sessionId, screen: prev?.sessionId === sessionId ? !!prev.screen : false } });
}

// startVoiceCall calls sessionId. Without provider the server uses the plan;
// a paid provider is passed only from the explicit paid button.
export function startVoiceCall(sessionId, { provider, screen } = {}) {
  const current = store.get().voiceCall;
  if (!sessionId || (current?.active && current.sessionId !== sessionId)) return false;
  if (current?.active) return false;
  if (!controller || controller.sessionId !== sessionId) {
    controller = new VoiceCallController({
      sessionId,
      fetch: deps.fetch,
      WebSocket: deps.WebSocket,
      createAudio: deps.createAudio,
      fallback: deps.fallback,
      onFallback: nativeFailed,
      onState: (state) => publish(sessionId, state),
    });
  }
  callName = partyName(sessionId);
  callSession = sessionId;
  const showScreen = screen ?? !!store.get().isMobile;
  setState({ voiceCall: { ...controller.state(), sessionId, screen: showScreen } });
  controller.start(provider ? { provider } : {});
  return true;
}

// adoptNativeCall shows again the call the iOS app kept running while this
// document was reloaded, so it can still be muted and hung up from here.
export async function adoptNativeCall() {
  if (!deps.adopt || !nativeBridge?.active) return false;
  let snap = null;
  try { snap = await nativeBridge.active(); } catch { return false; }
  if (!snap?.id || !snap.sessionId || !snap.callId || store.get().voiceCall?.active) return false;
  deps.adopt(snap.id);
  controller = new VoiceCallController({
    sessionId: snap.sessionId,
    fetch: deps.fetch,
    WebSocket: deps.WebSocket,
    createAudio: deps.createAudio,
    onState: (state) => publish(snap.sessionId, state),
  });
  callName = partyName(snap.sessionId);
  callSession = snap.sessionId;
  controller.adopt({ callId: snap.callId, muted: !!snap.muted });
  return true;
}

// voiceCallStream is the call's microphone, for the composer's level wash.
export function voiceCallStream() {
  return controller?.audio?.stream || null;
}

export function hangupVoiceCall() {
  return controller?.hangup();
}

export function setVoiceCallMuted(muted) {
  controller?.setMuted(muted);
}

// showVoiceCallScreen opens or leaves the phone's call screen; the call goes
// on either way.
export function showVoiceCallScreen(open) {
  const call = store.get().voiceCall;
  if (call) setState({ voiceCall: { ...call, screen: !!open } });
}

// dismissVoiceCall clears a finished or failed call from view.
export function dismissVoiceCall() {
  const call = store.get().voiceCall;
  if (!call || call.active) return;
  setState({ voiceCall: null });
}

// callOwner is the owner row's ☎: it calls the owner's conversation, or
// returns to the call already running with it. It reports whether a call is
// now with this owner, so the desktop can open the conversation that shows it.
export function callOwner(owner) {
  const sessionId = owner?.session_id;
  if (!sessionId) return false;
  const current = store.get().voiceCall;
  if (current?.active) {
    if (current.sessionId !== sessionId) return false;
    showVoiceCallScreen(!!store.get().isMobile);
    return true;
  }
  return startVoiceCall(sessionId);
}
