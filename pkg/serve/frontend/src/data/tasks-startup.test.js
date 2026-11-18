import { test, expect, beforeEach, afterEach } from 'bun:test';
import { setState } from './store.js';

// The bootstrap read of the task list and the first greeting of the
// invalidation socket used to fetch the list twice (741 KB each), and the
// three views of a session each read its tasks again.

let requests;
let sockets;
let revision;

class FakeSocket {
  constructor(url) { this.url = url; this.readyState = 1; sockets.push(this); }
  greet(rev) { this.onmessage({ data: JSON.stringify({ type: 'tasks_changed', revision: rev }) }); }
  close() {}
}

const saved = {};
const KEYS = ['location', 'WebSocket', 'localStorage', 'fetch'];

afterEach(() => {
  for (const key of KEYS) {
    if (saved[key] === undefined) delete globalThis[key]; else globalThis[key] = saved[key];
  }
});

beforeEach(() => {
  for (const key of KEYS) saved[key] = globalThis[key];
  requests = [];
  sockets = [];
  revision = 7;
  globalThis.location = { protocol: 'http:', host: 'moa.test' };
  globalThis.WebSocket = FakeSocket;
  globalThis.localStorage = globalThis.localStorage || { getItem: () => null, setItem() {} };
  globalThis.fetch = async (url) => {
    requests.push(String(url));
    const body = String(url).includes('/api/tasks?')
      ? { tasks: [], counts: {}, revision }
      : { requests: [], checklist: [], scheduled: [] };
    return { ok: true, status: 200, headers: new Headers(), json: async () => body, text: async () => JSON.stringify(body) };
  };
  setState({ sessions: {}, activeSession: null });
});

const lists = () => requests.filter((u) => u.startsWith('/api/tasks?'));
const settle = () => new Promise((resolve) => setTimeout(resolve, 400));

test('a greeting that carries the revision already read costs no second list request', async () => {
  const tasks = await import('./tasks.js?greeting-same');
  tasks.startTasksSync();
  expect(sockets).toHaveLength(1);
  sockets[0].greet(7);
  await settle();
  expect(lists()).toHaveLength(1);

  sockets[0].greet(8);
  await settle();
  expect(lists()).toHaveLength(2);
});

test('a greeting that reveals a change made after the first read still refreshes', async () => {
  const tasks = await import('./tasks.js?greeting-newer');
  tasks.startTasksSync();
  sockets[0].greet(9);
  await settle();
  expect(lists()).toHaveLength(2);
});

test('three views of one session read its tasks once', async () => {
  const tasks = await import('./tasks.js?session-views');
  const stops = [tasks.watchSessionTasks('s1'), tasks.watchSessionTasks('s1'), tasks.watchSessionTasks('s1')];
  await settle();
  expect(requests.filter((u) => u.includes('/api/sessions/s1/tasks'))).toHaveLength(1);

  stops.forEach((stop) => stop());
  tasks.watchSessionTasks('s1');
  await settle();
  expect(requests.filter((u) => u.includes('/api/sessions/s1/tasks'))).toHaveLength(2);
});
