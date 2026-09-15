import { afterEach, expect, test } from 'bun:test';
import {
  recoverNativeAuthorization, resetNativeAuthorizationRecoveryForTests,
} from './native-auth.js';

afterEach(() => resetNativeAuthorizationRecoveryForTests());

test('native authorization recovery renews the HttpOnly session then reloads', async () => {
  let renewals = 0;
  let reloads = 0;
  const bridge = { reauthorize: async () => { renewals += 1; } };
  const navigation = { reload: () => { reloads += 1; } };

  const first = recoverNativeAuthorization(bridge, navigation);
  const second = recoverNativeAuthorization(bridge, navigation);

  expect(second).toBe(first);
  await expect(first).resolves.toBe(true);
  expect(renewals).toBe(1);
  expect(reloads).toBe(1);
});

test('a revoked native device returns to the local pairing page', async () => {
  const error = new Error('revoked');
  error.code = 'not_paired';
  const destinations = [];
  const bridge = { reauthorize: () => Promise.reject(error) };
  const navigation = { replace: (url) => destinations.push(url) };

  await expect(recoverNativeAuthorization(bridge, navigation)).resolves.toBe(false);
  expect(destinations).toEqual(['capacitor://localhost']);
});

test('ordinary browsers do not enter native recovery', () => {
  expect(recoverNativeAuthorization(undefined, {})).toBeNull();
});
