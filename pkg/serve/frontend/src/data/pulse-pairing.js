const PULSE_PAIRING_PREFIX = 'moa-pulse-pair-v1:';

function base64URL(bytes) {
  let binary = '';
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize));
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// The QR contains only the server origin and the one-time pairing payload.
// Auth cookies, the Serve token, and resulting device credentials never leave
// the browser through this envelope.
export async function createPulsePairing(fetchImpl = fetch) {
  const response = await fetchImpl('/api/pulse/pairings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Moa-Request': '1' },
    body: '{}',
    cache: 'no-store',
    credentials: 'same-origin',
  });
  if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
  return response.json();
}

export function encodePulsePairingEnvelope(serverURL, pairingPayload) {
  if (typeof serverURL !== 'string' || typeof pairingPayload !== 'string') {
    throw new TypeError('server URL and pairing payload must be strings');
  }
  const json = JSON.stringify({ server_url: serverURL, pairing_payload: pairingPayload });
  return PULSE_PAIRING_PREFIX + base64URL(new TextEncoder().encode(json));
}
