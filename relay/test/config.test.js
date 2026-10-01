import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';

const read = (p) => readFileSync(new URL('../' + p, import.meta.url), 'utf8');

test('wrangler.json: no logs, no public URLs, no data bindings', () => {
  const w = JSON.parse(read('wrangler.json'));
  assert.equal(w.name, 'moa-push-relay');
  assert.equal(w.main, 'src/index.js');
  assert.equal(w.workers_dev, false);
  assert.equal(w.preview_urls, false);
  assert.deepEqual(w.observability, { enabled: false, logs: { enabled: false, invocation_logs: false } });
  const allowed = ['name', 'main', 'compatibility_date', 'workers_dev', 'preview_urls', 'observability', 'ratelimits', 'vars'];
  for (const k of Object.keys(w)) assert.ok(allowed.includes(k), `unexpected key ${k}`);
  for (const k of ['kv_namespaces', 'd1_databases', 'r2_buckets', 'durable_objects', 'queues', 'services',
    'tail_consumers', 'logpush', 'analytics_engine_datasets', 'routes', 'route', 'hyperdrive', 'vectorize']) {
    assert.ok(!(k in w), k);
  }
  assert.deepEqual(Object.keys(w.vars), ['VERSION']);
  const limits = Object.fromEntries(w.ratelimits.map((r) => [r.name, r.simple]));
  assert.deepEqual(limits, { SEND_LIMITER: { limit: 10, period: 60 }, REGISTER_LIMITER: { limit: 3, period: 60 } });
  assert.equal(new Set(w.ratelimits.map((r) => r.namespace_id)).size, 2);
});

test('src contains no console output', () => {
  const dir = new URL('../src/', import.meta.url);
  for (const f of readdirSync(dir, { recursive: true })) {
    if (String(f).endsWith('.js')) assert.ok(!read('src/' + f).includes('console.'), f);
  }
});

test('package.json: only a pinned wrangler dev dependency', () => {
  const p = JSON.parse(read('package.json'));
  assert.equal(p.dependencies, undefined);
  assert.deepEqual(Object.keys(p.devDependencies), ['wrangler']);
  assert.match(p.devDependencies.wrangler, /^4\.\d+\.\d+$/);
});
