import { claimWakeLock } from './wake-lock.js';

export const MIN_RECORDING_MS = 400;
export const MIN_BLOB_BYTES = 256;

let microphoneLease = null;
let microphoneGeneration = 0;

function claimMicrophone(owner) {
  if (microphoneLease) return null;
  const lease = { owner, generation: ++microphoneGeneration };
  microphoneLease = lease;
  return lease;
}

function ownsMicrophone(lease) {
  return microphoneLease === lease;
}

function releaseMicrophone(lease) {
  if (ownsMicrophone(lease)) microphoneLease = null;
}

function stopTracks(stream) {
  stream?.getTracks?.().forEach((track) => track.stop());
}

function isUsableMime(type) {
  if (!type) return false;
  const value = type.toLowerCase();
  return !/codecs=\s*$/.test(value) && /^(audio|video)\//.test(value);
}

function extForType(type) {
  const value = (type || '').toLowerCase();
  if (value.includes('webm')) return 'webm';
  if (value.includes('ogg')) return 'ogg';
  if (value.includes('wav')) return 'wav';
  if (value.includes('mp4') || value.includes('m4a') || value.includes('aac') || value.includes('mpeg') || value.includes('mpga')) return 'mp4';
  return '';
}

function defaultAppleWebKit() {
  if (typeof navigator === 'undefined') return false;
  const userAgent = navigator.userAgent || '';
  return /iP(hone|ad|od)/.test(userAgent)
    || /^((?!chrome|android|crios|fxios).)*safari/i.test(userAgent);
}

function preferredTypes(isAppleWebKit) {
  return isAppleWebKit
    ? ['audio/mp4;codecs=mp4a.40.2', 'audio/mp4', 'audio/aac', 'audio/webm;codecs=opus', 'audio/webm']
    : ['audio/webm;codecs=opus', 'audio/webm', 'audio/ogg;codecs=opus', 'audio/mp4;codecs=mp4a.40.2', 'audio/mp4'];
}

async function microphoneError(error) {
  const name = error?.name || '';
  if (name === 'NotAllowedError' || name === 'SecurityError') {
    let denied = false;
    try {
      const permission = await globalThis.navigator?.permissions?.query?.({ name: 'microphone' });
      denied = permission?.state === 'denied';
    } catch { /* permission state is best effort */ }
    if (denied) return 'Microphone access denied. Allow it in your browser settings.';
    return 'Microphone unavailable right now. Try again in a moment.';
  }
  if (name === 'NotFoundError') return 'No microphone found.';
  return 'Could not start recording: ' + (error?.message || String(error));
}

/**
 * A per-hook voice capture state machine. Each start creates an attempt with a
 * unique identity; callbacks may only mutate the attempt that installed them.
 * The physical microphone lease is module-global because multiple composers can
 * exist in one page, while UI/transcription state remains local to this object.
 */
export class VoiceCaptureController {
  constructor(options = {}) {
    this.getUserMedia = options.getUserMedia || ((constraints) => navigator.mediaDevices.getUserMedia(constraints));
    this.MediaRecorder = options.MediaRecorder || globalThis.MediaRecorder;
    this.fetch = options.fetch || globalThis.fetch;
    this.FormData = options.FormData || globalThis.FormData;
    this.Blob = options.Blob || globalThis.Blob;
    this.now = options.now || Date.now;
    this.isAppleWebKit = options.isAppleWebKit ?? defaultAppleWebKit();
    this.claimWakeLock = options.claimWakeLock || claimWakeLock;
    this.callbacks = {};
    this.setCallbacks(options);
    this.attempt = null;
    this.nextAttempt = 0;
    this.disposed = false;
  }

  setCallbacks({ onState, onTranscript, onError, onComplete } = {}) {
    this.callbacks = { onState, onTranscript, onError, onComplete };
  }

  isCurrent(attempt) {
    return !this.disposed && this.attempt === attempt;
  }

  notifyState(attempt, state) {
    if (this.isCurrent(attempt)) this.callbacks.onState?.(state);
  }

  reportError(attempt, message) {
    if (this.isCurrent(attempt)) this.callbacks.onError?.(message);
  }

  complete(attempt) {
    if (!this.isCurrent(attempt)) return;
    attempt.phase = 'complete';
    this.attempt = null;
    this.callbacks.onComplete?.();
  }

  releaseLease(attempt) {
    if (attempt.lease) {
      releaseMicrophone(attempt.lease);
      attempt.lease = null;
    }
  }

  releaseWakeClaim(attempt) {
    if (attempt.wakeClaim) {
      attempt.wakeClaim();
      attempt.wakeClaim = null;
    }
  }

  start() {
    if (this.disposed || this.attempt || !this.MediaRecorder || !this.getUserMedia) return;
    const attempt = {
      id: ++this.nextAttempt,
      lease: claimMicrophone(this),
      stream: null,
      recorder: null,
      chunks: [],
      startedAt: 0,
      discard: false,
      wakeClaim: null,
      phase: 'opening',
    };
    if (!attempt.lease) {
      this.callbacks.onError?.('Microphone is already in use by another recording.');
      return;
    }
    this.attempt = attempt;
    this.notifyState(attempt, { recording: false, transcribing: false });
    this.open(attempt);
  }

  async open(attempt) {
    let stream;
    try {
      stream = await this.getUserMedia({ audio: true });
    } catch (error) {
      if (!this.isCurrent(attempt)) return;
      const message = await microphoneError(error);
      // Permission queries are asynchronous too. A cancellation or newer
      // attempt during that query must make this rejection a no-op.
      if (!this.isCurrent(attempt)) return;
      this.releaseLease(attempt);
      this.notifyState(attempt, { recording: false, transcribing: false });
      this.reportError(attempt, message);
      this.complete(attempt);
      return;
    }

    if (!this.isCurrent(attempt) || attempt.discard || !ownsMicrophone(attempt.lease)) {
      stopTracks(stream);
      this.releaseLease(attempt);
      return;
    }

    attempt.stream = stream;
    const requestedMime = preferredTypes(this.isAppleWebKit)
      .find((type) => this.MediaRecorder.isTypeSupported?.(type)) || '';
    let recorder;
    try {
      recorder = new this.MediaRecorder(stream, requestedMime ? { mimeType: requestedMime } : {});
    } catch (error) {
      stopTracks(stream);
      this.releaseLease(attempt);
      this.reportError(attempt, 'Could not start recording: ' + (error?.message || String(error)));
      this.complete(attempt);
      return;
    }

    attempt.recorder = recorder;
    attempt.startedAt = this.now();
    recorder.ondataavailable = (event) => {
      if (event.data?.size > 0) attempt.chunks.push(event.data);
    };
    recorder.onerror = (event) => {
      if (!this.isCurrent(attempt)) return;
      attempt.discard = true;
      this.releaseWakeClaim(attempt);
      this.notifyState(attempt, { recording: false, transcribing: false });
      this.reportError(attempt, 'Recording failed: ' + (event.error?.message || 'unknown error'));
      if (recorder.state !== 'inactive') recorder.stop();
      else this.stopped(attempt, requestedMime);
    };
    recorder.onstop = () => this.stopped(attempt, requestedMime);

    try {
      recorder.start(1000);
    } catch (error) {
      stopTracks(stream);
      this.releaseLease(attempt);
      this.reportError(attempt, 'Could not start recording: ' + (error?.message || String(error)));
      this.complete(attempt);
      return;
    }
    if (!this.isCurrent(attempt) || attempt.discard) {
      if (recorder.state !== 'inactive') recorder.stop();
      return;
    }
    attempt.wakeClaim = this.claimWakeLock();
    attempt.phase = 'recording';
    this.notifyState(attempt, { recording: true, transcribing: false });
  }

  stop() {
    this.finish(false);
  }

  cancel() {
    this.finish(true);
  }

  finish(discard) {
    const attempt = this.attempt;
    if (!attempt) return;
    if (discard) attempt.discard = true;
    attempt.phase = attempt.recorder ? 'stopping' : 'cancelled';
    this.releaseWakeClaim(attempt);
    this.notifyState(attempt, { recording: false, transcribing: false });
    if (!attempt.recorder) {
      // getUserMedia is still pending. It is now stale; its eventual stream is
      // stopped by open(), and releasing lets a new attempt claim the mic now.
      this.releaseLease(attempt);
      this.complete(attempt);
      return;
    }
    if (attempt.recorder.state !== 'inactive') attempt.recorder.stop();
  }

  async stopped(attempt, requestedMime) {
    stopTracks(attempt.stream);
    this.releaseWakeClaim(attempt);
    // onstop is the final MediaRecorder callback. Only now is another page
    // attempt allowed to acquire the physical microphone.
    this.releaseLease(attempt);
    if (!this.isCurrent(attempt)) return;
    this.notifyState(attempt, { recording: false, transcribing: false });

    const duration = this.now() - attempt.startedAt;
    if (attempt.discard || duration < MIN_RECORDING_MS || attempt.chunks.length === 0) {
      this.complete(attempt);
      return;
    }

    const chunkType = attempt.chunks.find((chunk) => isUsableMime(chunk.type))?.type || '';
    const reportedType = isUsableMime(attempt.recorder.mimeType) ? attempt.recorder.mimeType : '';
    const effectiveType = chunkType || reportedType || (isUsableMime(requestedMime) ? requestedMime : '');
    const blob = new this.Blob(attempt.chunks, effectiveType ? { type: effectiveType } : {});
    if (blob.size < MIN_BLOB_BYTES) {
      this.reportError(attempt, 'No audio captured. Make sure the mic isn\'t in use by another app, then try again.');
      this.complete(attempt);
      return;
    }
    const extension = extForType(effectiveType || blob.type);
    if (!extension) {
      this.reportError(attempt, 'This browser produced an audio format we can\'t transcribe. Try a different browser.');
      this.complete(attempt);
      return;
    }

    attempt.phase = 'transcribing';
    this.notifyState(attempt, { recording: false, transcribing: true });
    try {
      const form = new this.FormData();
      form.append('audio', blob, `recording.${extension}`);
      const response = await this.fetch('/api/transcribe', {
        method: 'POST', headers: { 'X-Moa-Request': '1' }, body: form,
      });
      if (!this.isCurrent(attempt) || attempt.discard) return;
      if (!response.ok) {
        const detail = (await response.text()).trim();
        if (!this.isCurrent(attempt) || attempt.discard) return;
        this.reportError(attempt, detail || `Transcription failed (HTTP ${response.status})`);
        return;
      }
      const data = await response.json();
      if (!this.isCurrent(attempt) || attempt.discard) return;
      const text = (data.text || '').trim();
      if (text) this.callbacks.onTranscript?.(text);
      else this.reportError(attempt, 'No speech detected. Try again a bit closer to the mic.');
    } catch (error) {
      this.reportError(attempt, 'Transcription error: ' + (error?.message || String(error)));
    } finally {
      if (this.isCurrent(attempt)) {
        this.notifyState(attempt, { recording: false, transcribing: false });
        this.complete(attempt);
      }
    }
  }

  dispose() {
    this.disposed = true;
    const attempt = this.attempt;
    if (!attempt) return;
    attempt.discard = true;
    attempt.phase = 'cancelled';
    this.releaseWakeClaim(attempt);
    if (!attempt.recorder) {
      this.releaseLease(attempt);
      this.attempt = null;
      return;
    }
    if (attempt.recorder.state !== 'inactive') attempt.recorder.stop();
  }
}

export function __microphoneLeaseForTests() {
  return microphoneLease && { generation: microphoneLease.generation };
}
