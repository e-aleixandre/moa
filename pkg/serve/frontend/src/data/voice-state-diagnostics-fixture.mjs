import { pathToFileURL } from 'node:url';
import { VoiceCallController } from './voice-call.js';
import { workletSource } from './voice-call-audio.js';

export async function fixture(active = true) {
  let c, socket, tick;
  const sent = [], phases = [], types = {};
  class Processor {
    constructor() { this.port = { postMessage: (event) => c.playback(event) }; }
  }
  new Function('AudioWorkletProcessor', 'registerProcessor', 'sampleRate', workletSource())(
    Processor, (name, cls) => { types[name] = cls; }, 24000);
  const player = new types['moa-call-playback']();
  const audio = {
    async start() {}, stop() {},
    play(pcm) { player.port.onmessage({ data: { type: 'pcm', pcm } }); },
    interrupt() { player.port.onmessage({ data: { type: 'clear' } }); },
  };
  class Socket {
    constructor() { this.readyState = 0; this.bufferedAmount = 0; socket = this; }
    send(v) { sent.push(JSON.parse(v)); }
    close() { this.readyState = 3; }
  }
  const original = globalThis.setInterval;
  globalThis.setInterval = (fn) => { tick = fn; return 1; };
  c = new VoiceCallController({ sessionId: 'synthetic', WebSocket: Socket, audio,
    fetch: async () => ({ status: 201, json: async () => ({ call_id: 'synthetic' }) }),
    onState: (s) => phases.push(s.phase),
  });
  try {
    const start = c.start();
    for (let i = 0; i < 8; i++) await Promise.resolve();
    socket.readyState = 1; socket.onopen(); await start;
    c.receive(JSON.stringify({ type: 'state', state: 'listening', ...(active ? {
      audio_diagnostics: { v: 1, active: true, interval_ms: 1000 },
    } : {}) }));
  } finally { globalThis.setInterval = original; }
  return { c, socket, sent, phases, flush: () => tick?.(),
    feed(samples = 480) { c.receive(new Int16Array(samples).fill(8192).buffer); },
    render(n = 1) { for (let i = 0; i < n; i++) player.process([], [[new Float32Array(128)]]); },
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  for (const bounded of [false, true]) {
    const h = await fixture();
    try {
      if (bounded) {
        for (let i = 0; i < 40; i++) { h.feed(); h.render(5); }
      } else {
        h.feed(); h.render(5); h.feed(24000); h.render();
        h.c.receive(JSON.stringify({ type: 'state', state: 'listening', interrupt: true }));
        h.feed(24000); h.render();
        h.c.receive(JSON.stringify({ type: 'state', state: 'listening' }));
      }
      h.flush();
      if (h.sent.length !== 1) throw new Error('missing synthetic report');
      process.stdout.write(JSON.stringify(h.sent[0]) + '\n');
    } finally { h.c.finish('hangup'); }
  }
}
