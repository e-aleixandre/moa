// ring-tone.js — the incoming call's tone, synthesised with Web Audio so it
// ships no asset. Best effort: a browser that has not had a tap yet keeps the
// context suspended and the call simply rings in silence on screen.

import { ringExpired } from './call-requests-model.js';

// Only a Ring call sounds, and only while the app is on screen.
export function shouldRing(request, visible, now = Date.now()) {
  return request?.level === 'ring' && request?.state === 'ringing' && !!visible && !ringExpired(request, now);
}

// startRingTone plays a two-note ring every 2.4 s and returns stop().
export function startRingTone(AudioCtx = globalThis.AudioContext || globalThis.webkitAudioContext) {
  if (typeof AudioCtx !== 'function') return () => {};
  let ctx;
  try { ctx = new AudioCtx(); } catch { return () => {}; }
  ctx.resume?.().catch?.(() => {});
  const burst = () => {
    const t0 = ctx.currentTime;
    [[0, 440], [0.22, 554]].forEach(([at, hz]) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = 'sine';
      osc.frequency.value = hz;
      gain.gain.setValueAtTime(0.0001, t0 + at);
      gain.gain.exponentialRampToValueAtTime(0.18, t0 + at + 0.03);
      gain.gain.exponentialRampToValueAtTime(0.0001, t0 + at + 0.4);
      osc.connect(gain).connect(ctx.destination);
      osc.start(t0 + at);
      osc.stop(t0 + at + 0.45);
    });
  };
  burst();
  const timer = setInterval(burst, 2400);
  return () => {
    clearInterval(timer);
    try { ctx.close?.(); } catch { /* closed */ }
  };
}
