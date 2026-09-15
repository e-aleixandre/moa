import { beforeEach, expect, test } from 'bun:test';
import { store, SHARE_CLOSED, setState } from './store.js';
import { __resetSharesForTests, dismissShare } from './share.js';
import { decodeNativeShare, installNativeShareNavigation } from './native-share.js';

const ID = 'b3b62f19-37aa-4f58-86ad-5e40882f6314';

function encodedShare(overrides = {}) {
  return {
    id: ID,
    title: '',
    text: 'Look at this',
    url: 'https://example.test',
    files: [{
      name: 'note.txt',
      mime: 'text/plain',
      size: 3,
      data: Buffer.from('moa').toString('base64'),
    }],
    ...overrides,
  };
}

beforeEach(() => {
  __resetSharesForTests();
  setState({ share: SHARE_CLOSED });
});

test('native files become ordinary File objects for the composer', () => {
  const share = decodeNativeShare(encodedShare());
  expect(share.id).toBe(ID);
  expect(share.files).toHaveLength(1);
  expect(share.files[0]).toBeInstanceOf(File);
  expect(share.files[0].name).toBe('note.txt');
  expect(share.files[0].type.startsWith('text/plain')).toBe(true);
  expect(share.files[0].size).toBe(3);
});

test('a malformed native payload is retained instead of acknowledged', async () => {
  let acknowledgements = 0;
  const inbox = {
    async peek() { return { share: encodedShare({ files: [{ name: 'bad', size: 4, data: 'eA==' }] }) }; },
    async acknowledge() { acknowledgements += 1; },
  };
  const stop = installNativeShareNavigation({ inbox, doc: null });
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(store.get().share.status).toBe('idle');
  expect(acknowledgements).toBe(0);
  stop();
});

test('the App Group copy remains until the owner places or dismisses it', async () => {
  const acknowledged = [];
  const inbox = {
    async peek() { return { share: encodedShare() }; },
    async acknowledge(id) { acknowledged.push(id); },
  };
  const stop = installNativeShareNavigation({ inbox, doc: null });
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(store.get().share.status).toBe('ready');
  expect(acknowledged).toEqual([]);

  dismissShare();
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(acknowledged).toEqual([ID]);
  stop();
});

test('a regular browser with no native bridge is unchanged', () => {
  expect(typeof installNativeShareNavigation({ inbox: null, doc: null })).toBe('function');
  expect(store.get().share.status).toBe('idle');
});
