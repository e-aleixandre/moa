// voice-call-native.js — a call whose socket and audio live in the app.
//
// The paired iOS app injects window.MoaNativeCall. With it, the call's
// WebSocket, microphone and speaker run in the app process as a system call
// (CallKit), so it goes on with the screen locked, in headphones and in the
// car, even while this page is suspended. This module adapts that bridge to
// the controller's two boundaries — a WebSocket and an audio engine — so
// VoiceCallController is the same for every client. Audio never reaches
// this page: the app sends the microphone and plays the provider itself,
// and only the JSON control frames are mirrored here for the interface.
//
// Bridge (v1): open(url, {name, sessionId}) → id; send(id, text); close(id);
// startAudio(id) → Promise; stopAudio(id); setMuted(id, muted);
// active() → Promise<{id, sessionId, callId, muted} | null>, the call the app
// is still carrying when this document loads (a reload, or the page killed
// in the background). Events arrive through bridge.onevent({id, type, data})
// with type open | message | close | playback | mute.

export function nativeCallBridge(win = globalThis.window) {
  const b = win?.MoaNativeCall;
  return b && b.version >= 1 && typeof b.open === 'function' ? b : null;
}

export function createNativeCallDeps(bridge, { name = () => '', sessionId = () => '' } = {}) {
  const sockets = new Map();
  const engines = new Map();

  bridge.onevent = (ev) => {
    const id = ev?.id;
    if (ev?.type === 'playback') {
      engines.get(id)?.onPlayback?.({ type: ev.data });
      return;
    }
    const ws = sockets.get(id);
    if (!ws) return;
    switch (ev.type) {
      case 'open':
        ws.readyState = 1;
        ws.onopen?.();
        return;
      case 'message':
        if (typeof ev.data === 'string') ws.onmessage?.({ data: ev.data });
        return;
      case 'mute':
        // Muted from the system call screen or a headset: the app already
        // told the server; the interface only follows.
        ws.onmessage?.({ data: JSON.stringify({ type: 'native_mute', muted: !!ev.data }) });
        return;
      case 'close':
        if (ws.readyState === 3) return;
        ws.readyState = 3;
        sockets.delete(id);
        ws.onclose?.();
        return;
      default:
    }
  };

  let current = null;
  let adopting = null;

  class NativeCallSocket {
    constructor(url) {
      this.readyState = 0;
      this.bufferedAmount = 0;
      this.binaryType = 'arraybuffer';
      this.onopen = null;
      this.onclose = null;
      this.onerror = null;
      this.onmessage = null;
      if (adopting) {
        // The app's call already runs: this socket only mirrors it.
        this.id = adopting;
        adopting = null;
        queueMicrotask(() => {
          if (this.readyState !== 0) return;
          this.readyState = 1;
          this.onopen?.();
        });
      } else {
        this.id = bridge.open(String(url), { name: name() || 'moa', sessionId: sessionId() || '' });
      }
      sockets.set(this.id, this);
      current = this.id;
    }

    send(data) {
      // Only controls travel from here; audio is the app's.
      if (this.readyState === 1 && typeof data === 'string') bridge.send(this.id, data);
    }

    close() {
      if (this.readyState === 3) return;
      this.readyState = 3;
      sockets.delete(this.id);
      bridge.close(this.id);
    }
  }

  function createAudio() {
    let id = null;
    let stopped = false;
    return {
      stream: null,
      supported: true,
      prepare() {},
      async start(_onPCM, { onPlayback } = {}) {
        if (stopped) throw new Error('audio stopped');
        id = current;
        engines.set(id, { onPlayback });
        await bridge.startAudio(id);
        if (stopped) throw new Error('audio stopped');
      },
      play() {},
      // The app clears its own playback on the server's barge-in as it
      // arrives; repeating it from this mirror, later, would cut the next
      // reply instead.
      interrupt() {},
      setMuted(muted) { if (id !== null) bridge.setMuted(id, !!muted); },
      stop() {
        if (stopped) return;
        stopped = true;
        if (id !== null) {
          engines.delete(id);
          bridge.stopAudio(id);
        }
      },
    };
  }

  // adopt makes the next socket attach to the app's running call id.
  function adopt(id) { adopting = id; }

  return { WebSocket: NativeCallSocket, createAudio, adopt, supported: true };
}
