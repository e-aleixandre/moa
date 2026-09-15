let recovery = null;

// A browser session is intentionally short-lived. On a 401 the native shell
// can renew it from the durable Keychain credential without exposing either
// value to page JavaScript. A revoked device returns to the local pairing page.
export function recoverNativeAuthorization(
  bridge = globalThis.MoaNativeAuth,
  navigation = globalThis.location,
) {
  if (typeof bridge?.reauthorize !== 'function') return null;
  if (recovery) return recovery;
  recovery = Promise.resolve()
    .then(() => bridge.reauthorize())
    .then(() => {
      navigation?.reload?.();
      return true;
    })
    .catch((error) => {
      if (error?.code === 'not_paired') {
        navigation?.replace?.('capacitor://localhost');
      }
      return false;
    })
    .finally(() => { recovery = null; });
  return recovery;
}

export function resetNativeAuthorizationRecoveryForTests() {
  recovery = null;
}
