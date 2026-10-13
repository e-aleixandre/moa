// rewind-edit.test.js — run with `bun test`
//
// What a rewind of one of your own messages hands back to the composer: the
// text, and the attachments fetched again from the store.
import { test, expect, beforeEach } from 'bun:test';

const { store, setState } = await import('./store.js');
const { rewoundFiles, placeRewoundMessage } = await import('./rewind-edit.js');

beforeEach(() => {
  setState({ composerDrops: {} });
});

const stored = (id, name, mime, type = 'image') => ({ type, attachment_id: id, filename: name, mime_type: mime });

test('rewoundFiles downloads stored attachments as Files, keeping name and mime', async () => {
  const calls = [];
  const fetchImpl = async (url) => {
    calls.push(url);
    return new Response(new Blob(['bytes'], { type: 'image/png' }), { status: 200 });
  };
  const { files, missing } = await rewoundFiles('s 1', [stored('a/1', 'shot.png', 'image/png')], { fetch: fetchImpl });
  expect(calls).toEqual(['/api/sessions/s%201/attachments/a%2F1']);
  expect(missing).toEqual([]);
  expect(files).toHaveLength(1);
  expect(files[0].name).toBe('shot.png');
  expect(files[0].type).toBe('image/png');
  expect(await files[0].text()).toBe('bytes');
});

test('rewoundFiles decodes legacy inline data without a request', async () => {
  const fetchImpl = async () => { throw new Error('no request expected'); };
  const { files } = await rewoundFiles('s', [{ type: 'image', data: btoa('png!'), mime_type: 'image/png', filename: 'old.png' }], { fetch: fetchImpl });
  expect(files).toHaveLength(1);
  expect(await files[0].text()).toBe('png!');
});

test('a blob that is gone is reported by name and does not stop the others', async () => {
  const fetchImpl = async (url) => (url.includes('gone')
    ? new Response('nope', { status: 404 })
    : new Response(new Blob(['ok']), { status: 200 }));
  const { files, missing } = await rewoundFiles('s', [
    stored('gone', 'lost.png', 'image/png'),
    stored('here', 'notes.txt', 'text/plain', 'document'),
    { type: 'image', mime_type: 'image/png' },
  ], { fetch: fetchImpl });
  expect(files.map((f) => f.name)).toEqual(['notes.txt']);
  expect(missing).toEqual(['lost.png', 'image']);
});

test('a network error counts as missing too', async () => {
  const fetchImpl = async () => { throw new TypeError('offline'); };
  const { files, missing } = await rewoundFiles('s', [stored('a', 'a.png', 'image/png')], { fetch: fetchImpl });
  expect(files).toEqual([]);
  expect(missing).toEqual(['a.png']);
});

test('placeRewoundMessage fills the composer drop and asks for focus', () => {
  const file = new File(['x'], 'a.png', { type: 'image/png' });
  placeRewoundMessage('s', { text: 'my message', files: [file] });
  const drop = store.get().composerDrops.s;
  expect(drop.text).toBe('my message');
  expect(drop.files).toEqual([file]);
  expect(drop.focus).toBe(true);
});

test('a drop already waiting is kept: the message goes after it', () => {
  const first = new File(['1'], 'first.txt');
  setState({ composerDrops: { s: { id: 'x', text: 'earlier', files: [first] } } });
  const second = new File(['2'], 'second.txt');
  placeRewoundMessage('s', { text: 'rewound', files: [second] });
  const drop = store.get().composerDrops.s;
  expect(drop.text).toBe('earlier\nrewound');
  expect(drop.files).toEqual([first, second]);
});
