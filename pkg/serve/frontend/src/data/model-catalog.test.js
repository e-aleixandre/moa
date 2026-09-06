// model-catalog.test.js — the shared /api/models resource: one request for
// every consumer, success cached, failure retryable.
import { test, expect, beforeEach } from 'bun:test';

const { store, setState } = await import('./store.js');
const {
  MODEL_CATALOG_IDLE, catalogSpec, catalogThinkingPosition, ensureModelCatalog, loadModelCatalog, modelCatalog,
} = await import('./model-catalog.js');

const ASTRA = {
  id: 'gpt-6-astra', name: 'GPT-6 Astra', provider: 'openai', alias: 'astra',
  reasoning_efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
};

beforeEach(() => {
  setState({ modelCatalog: MODEL_CATALOG_IDLE });
});

test('concurrent consumers share one request and end ready', async () => {
  let calls = 0;
  let resolveModels;
  globalThis.fetch = () => {
    calls += 1;
    return new Promise(resolve => { resolveModels = resolve; });
  };

  const consumers = [loadModelCatalog(), loadModelCatalog(), loadModelCatalog()];
  expect(calls).toBe(1);
  expect(modelCatalog(store.get()).status).toBe('loading');

  resolveModels(new Response(JSON.stringify([ASTRA]), { status: 200 }));
  await Promise.all(consumers);

  expect(calls).toBe(1);
  expect(modelCatalog(store.get()).status).toBe('ready');
  expect(modelCatalog(store.get()).entries).toHaveLength(1);

  // A ready catalog answers from the store without another round trip.
  await loadModelCatalog();
  expect(calls).toBe(1);
});

test('a failed catalog stays unknown and the next open retries it', async () => {
  let calls = 0;
  globalThis.fetch = () => {
    calls += 1;
    if (calls === 1) return Promise.resolve(new Response('nope', { status: 500 }));
    return Promise.resolve(new Response(JSON.stringify([ASTRA]), { status: 200 }));
  };

  await loadModelCatalog();
  expect(modelCatalog(store.get())).toMatchObject({ status: 'error', entries: null });
  // The failure must not be permanent: an unknown catalog means no meter, so
  // leaving it stuck would hide the effort forever.
  expect(catalogThinkingPosition(modelCatalog(store.get()), { model: 'GPT-6 Astra', thinking: 'low' })).toBeNull();

  ensureModelCatalog();
  await loadModelCatalog();

  expect(calls).toBe(2);
  expect(modelCatalog(store.get()).status).toBe('ready');
});

test('an unready catalog reports no position instead of a made-up one', () => {
  expect(catalogThinkingPosition(MODEL_CATALOG_IDLE, { model: 'GPT-6 Astra', thinking: 'low' })).toBeNull();
  expect(catalogThinkingPosition({ status: 'loading', entries: null }, { thinking: 'low' })).toBeNull();
});

test('a ready catalog cannot infer the position of a missing model', async () => {
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify([ASTRA]), { status: 200 }));
  await loadModelCatalog();
  const current = { model: 'Unknown model', provider: 'openai', thinking: 'low' };
  expect(catalogThinkingPosition(modelCatalog(store.get()), current)).toBeNull();
  expect(catalogThinkingPosition({ status: 'ready', entries: [] }, current)).toBeNull();
});

test('a ready catalog maps Astra low to the zero position and keeps ordinary models', async () => {
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify([
    ASTRA,
    { id: 'gpt-5.6-terra', name: 'GPT-5.6 Terra', provider: 'openai', alias: 'terra' },
  ]), { status: 200 }));
  await loadModelCatalog();
  const catalog = modelCatalog(store.get());

  expect(catalogThinkingPosition(catalog, { model: 'GPT-6 Astra', provider: 'openai', thinking: 'low' })).toBe('off');
  expect(catalogThinkingPosition(catalog, { model: 'GPT-6 Astra', provider: 'openai', thinking: 'max' })).toBe('xhigh');
  for (const level of ['low', 'medium', 'high', 'xhigh']) {
    expect(catalogThinkingPosition(catalog, { model: 'GPT-5.6 Terra', provider: 'openai', thinking: level })).toBe(level);
  }
});

test('a spec resolves from the raw model a subagent carries, not only the display name', async () => {
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify([ASTRA]), { status: 200 }));
  await loadModelCatalog();
  const { entries } = modelCatalog(store.get());

  expect(catalogSpec(entries, 'GPT-6 Astra')?.catalogId).toBe('gpt-6-astra');
  expect(catalogSpec(entries, 'gpt-6-astra')?.catalogId).toBe('gpt-6-astra');
  expect(catalogSpec(entries, 'openai/gpt-6-astra')?.catalogId).toBe('gpt-6-astra');
  expect(catalogSpec(entries, 'astra')?.catalogId).toBe('gpt-6-astra');
  // The breadcrumb codename is NOT an identity the catalog knows.
  expect(catalogSpec(entries, 'Unknown model')).toBeUndefined();
});
