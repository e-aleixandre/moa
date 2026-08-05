import { test, expect, beforeEach } from 'bun:test';

beforeEach(() => { FakeRecorder.instances = []; });

let importCounter = 0;
async function freshModule() {
  importCounter++;
  return import(`./voice-capture.js?t=${importCounter}`);
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function stream() {
  const track = { stopped: false, stop() { this.stopped = true; } };
  return { track, getTracks: () => [track] };
}

class FakeRecorder {
  static instances = [];
  static isTypeSupported(type) { return type.startsWith('audio/webm'); }

  constructor(stream, options) {
    this.stream = stream;
    this.mimeType = options.mimeType || 'audio/webm';
    this.state = 'inactive';
    FakeRecorder.instances.push(this);
  }

  start() { this.state = 'recording'; }
  stop() { this.state = 'inactive'; }
  data(size = 300, type = 'audio/webm') { this.ondataavailable?.({ data: { size, type } }); }
  stopped() { return this.onstop?.(); }
  error(message = 'broken') { this.onerror?.({ error: { message } }); }
}

class FakeBlob {
  constructor(chunks, options = {}) {
    this.size = chunks.reduce((total, chunk) => total + chunk.size, 0);
    this.type = options.type || '';
  }
}

class FakeFormData {
  append() {}
}

function controllerOptions(overrides = {}) {
  return {
    MediaRecorder: FakeRecorder,
    Blob: FakeBlob,
    FormData: FakeFormData,
    fetch: async () => ({ ok: true, json: async () => ({ text: 'hello' }) }),
    now: () => 1000,
    isAppleWebKit: false,
    claimWakeLock: () => () => {},
    ...overrides,
  };
}

test('a stale getUserMedia resolution is stopped and cannot create a recorder for a newer attempt', async () => {
  const { VoiceCaptureController } = await freshModule();
  const first = deferred();
  const second = deferred();
  const requests = [first, second];
  const states = [];
  const controller = new VoiceCaptureController(controllerOptions({
    getUserMedia: () => requests.shift().promise,
    onState: (state) => states.push(state),
  }));

  controller.start();
  controller.cancel();
  controller.start();
  const newerStream = stream();
  second.resolve(newerStream);
  await Promise.resolve();
  await Promise.resolve();
  expect(FakeRecorder.instances).toHaveLength(1);

  const staleStream = stream();
  first.resolve(staleStream);
  await Promise.resolve();
  await Promise.resolve();
  expect(staleStream.track.stopped).toBe(true);
  expect(FakeRecorder.instances).toHaveLength(1);
  expect(states.at(-1)).toEqual({ recording: true, transcribing: false });
  controller.cancel();
  await FakeRecorder.instances[0].stopped();
});

test('a stale getUserMedia rejection cannot report an error into a newer attempt', async () => {
  const { VoiceCaptureController } = await freshModule();
  const first = deferred();
  const second = deferred();
  const errors = [];
  const controller = new VoiceCaptureController(controllerOptions({
    getUserMedia: () => [first, second].shift().promise,
    onError: (message) => errors.push(message),
  }));
  // Keep the queue outside the callback's array literal, which is recreated on each call.
  const requests = [first, second];
  controller.getUserMedia = () => requests.shift().promise;

  controller.start();
  controller.cancel();
  controller.start();
  second.resolve(stream());
  await Promise.resolve();
  await Promise.resolve();
  first.reject(new Error('old denial'));
  await Promise.resolve();
  await Promise.resolve();
  expect(errors).toEqual([]);
  controller.cancel();
  await FakeRecorder.instances.at(-1).stopped();
});

test('stale recorder callbacks cannot alter a later controller that owns the microphone', async () => {
  const { VoiceCaptureController } = await freshModule();
  const firstStates = [];
  const secondStates = [];
  const first = new VoiceCaptureController(controllerOptions({
    getUserMedia: async () => stream(), onState: (state) => firstStates.push(state),
  }));
  first.start();
  await Promise.resolve();
  await Promise.resolve();
  const oldRecorder = FakeRecorder.instances.at(-1);
  first.stop();
  await oldRecorder.stopped();

  const second = new VoiceCaptureController(controllerOptions({
    getUserMedia: async () => stream(), onState: (state) => secondStates.push(state),
  }));
  second.start();
  await Promise.resolve();
  await Promise.resolve();
  oldRecorder.error('late failure');
  expect(secondStates.at(-1)).toEqual({ recording: true, transcribing: false });
  expect(firstStates.at(-1)).toEqual({ recording: false, transcribing: false });
  second.cancel();
  await FakeRecorder.instances.at(-1).stopped();
});

test('cancelling after stop discards final audio and never transcribes it', async () => {
  const { VoiceCaptureController } = await freshModule();
  let transcriptions = 0;
  let fetches = 0;
  const controller = new VoiceCaptureController(controllerOptions({
    getUserMedia: async () => stream(),
    fetch: async () => { fetches++; return { ok: true, json: async () => ({ text: 'nope' }) }; },
    onTranscript: () => transcriptions++,
  }));
  controller.start();
  await Promise.resolve();
  await Promise.resolve();
  const recorder = FakeRecorder.instances.at(-1);
  recorder.data();
  controller.stop();
  controller.cancel();
  await recorder.stopped();
  expect(fetches).toBe(0);
  expect(transcriptions).toBe(0);
});

test('disposing after stop discards an in-flight transcription', async () => {
  const { VoiceCaptureController } = await freshModule();
  const response = deferred();
  let transcriptions = 0;
  let clock = 0;
  const controller = new VoiceCaptureController(controllerOptions({
    getUserMedia: async () => stream(),
    fetch: () => response.promise,
    now: () => clock,
    onTranscript: () => transcriptions++,
  }));
  controller.start();
  await Promise.resolve();
  await Promise.resolve();
  const recorder = FakeRecorder.instances.at(-1);
  recorder.data();
  clock = 500;
  controller.stop();
  const stopping = recorder.stopped();
  await Promise.resolve();
  controller.dispose();
  response.resolve({ ok: true, json: async () => ({ text: 'discarded' }) });
  await stopping;
  expect(transcriptions).toBe(0);
});

test('onstop releases only its microphone lease so another controller can record while transcription runs', async () => {
  const { VoiceCaptureController, __microphoneLeaseForTests } = await freshModule();
  const transcription = deferred();
  const firstStates = [];
  let clock = 0;
  const first = new VoiceCaptureController(controllerOptions({
    getUserMedia: async () => stream(),
    fetch: () => transcription.promise,
    now: () => clock,
    onState: (state) => firstStates.push(state),
  }));
  first.start();
  await Promise.resolve();
  await Promise.resolve();
  const recorder = FakeRecorder.instances.at(-1);
  recorder.data();
  clock = 500;
  first.stop();
  const stopping = recorder.stopped();
  await Promise.resolve();
  expect(__microphoneLeaseForTests()).toBe(null);
  expect(firstStates.at(-1)).toEqual({ recording: false, transcribing: true });

  const secondStates = [];
  const second = new VoiceCaptureController(controllerOptions({
    getUserMedia: async () => stream(), onState: (state) => secondStates.push(state),
  }));
  second.start();
  await Promise.resolve();
  await Promise.resolve();
  expect(secondStates.at(-1)).toEqual({ recording: true, transcribing: false });

  transcription.resolve({ ok: true, json: async () => ({ text: 'first' }) });
  await stopping;
  expect(secondStates.at(-1)).toEqual({ recording: true, transcribing: false });
  second.cancel();
  await FakeRecorder.instances.at(-1).stopped();
});
