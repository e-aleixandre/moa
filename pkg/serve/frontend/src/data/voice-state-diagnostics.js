// Temporary numeric-only trace. Clocks start at activation; they are not a
// sample clock or synchronized with native/server clocks. No content enters it.
// Rows: [seq, us, kind, cause, generation, before, after, aux]. Kinds 20/21/22
// are phase change / incoming state-clear / playback observation. Causes 1..7:
// playing, drained (not necessarily natural), server, interrupt, legacy speech,
// held server state, lifecycle. Before/after are indices in this phase table.
const phases = ['idle', 'starting', 'connecting', 'listening', 'speaking', 'consulting', 'ending', 'ended', 'failed'];

export function voiceStateDiagnostics(config, socket, generation) {
  if (socket?.audioDiagnosticsSupported === false) return null;
  if (config?.v !== 1 || config?.active !== true || config?.interval_ms !== 1000) return null;
  const origin = performance.now();
  let rows = [], sequence = 0, report = 0, dropped = 0, skipped = 0, windowStart = 0;
  let active = true;
  const flush = () => {
    if (!active) return;
    const end = Math.max(windowStart + 1, Math.ceil(performance.now() - origin));
    if (end > 86400000 || report >= 2147483647) { stop(); return; }
    if (socket.readyState !== 1 || (socket.bufferedAmount || 0) > 48000) { skipped++; return; }
    if (end - windowStart > 60000) {
      dropped += rows.length; rows = []; windowStart = end; skipped++; return;
    }
    const data = { type: 'audio_metrics', v: 1, client: 2, seq: ++report,
      window_start_ms: windowStart, window_ms: end - windowStart,
      trace_dropped: Math.min(dropped, 2147483647), reports_skipped: Math.min(skipped, 1000000), trace: rows };
    try {
      const json = JSON.stringify(data);
      if (json.length > 4096) throw new Error();
      socket.send(json);
    } catch { dropped += rows.length; skipped++; }
    rows = []; windowStart = end;
  };
  const timer = setInterval(flush, 1000);
  function stop() { if (!active) return; active = false; clearInterval(timer); rows = []; }
  return {
    event(kind, cause, before, after) {
      if (!active) return;
      const seq = ++sequence;
      if (seq > 2147483647) { stop(); return; }
      if (rows.length === 24) { dropped++; return; }
      rows.push([seq, Math.max(0, Math.floor((performance.now() - origin) * 1000)), kind, cause,
        Math.min(generation, 2147483647), Math.max(0, phases.indexOf(before)), Math.max(0, phases.indexOf(after)), 0]);
    },
    stop,
  };
}
