import { test, expect } from 'bun:test';
import { createPulsePairing, encodePulsePairingEnvelope } from './pulse-pairing.js';

function decodeEnvelope(envelope) {
  const encoded = envelope.slice('moa-pulse-pair-v1:'.length);
  const padded = encoded.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - encoded.length % 4) % 4);
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(padded), char => char.charCodeAt(0))));
}

test('Pulse pairing envelope is a base64url JSON envelope', () => {
  const envelope = encodePulsePairingEnvelope('https://moa.example', 'moa-pair-v1:id:secret');
  expect(envelope).toStartWith('moa-pulse-pair-v1:');
  expect(envelope).not.toMatch(/[+/=]/);
  expect(decodeEnvelope(envelope)).toEqual({
    server_url: 'https://moa.example',
    pairing_payload: 'moa-pair-v1:id:secret',
  });
});

test('Pulse pairing envelope preserves UTF-8 payloads', () => {
  expect(decodeEnvelope(encodePulsePairingEnvelope('https://møa.example', 'pair:🔐'))).toEqual({
    server_url: 'https://møa.example',
    pairing_payload: 'pair:🔐',
  });
});

test('Pulse pairing creation uses the owner request boundary without caching', async () => {
  let request;
  const result = await createPulsePairing(async (path, options) => {
    request = { path, options };
    return { ok: true, json: async () => ({ payload: 'moa-pair-v1:id:secret' }) };
  });

  expect(result).toEqual({ payload: 'moa-pair-v1:id:secret' });
  expect(request).toEqual({
    path: '/api/pulse/pairings',
    options: {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Moa-Request': '1' },
      body: '{}',
      cache: 'no-store',
      credentials: 'same-origin',
    },
  });
});
