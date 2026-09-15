export function nativeAuth(bridge = globalThis.MoaNativeAuth) {
  if (
    typeof bridge?.claim !== "function"
    || typeof bridge?.authorize !== "function"
  ) {
    throw new Error("Native device authentication is unavailable.");
  }
  return bridge;
}

// The durable credential crosses only the native URLSession/Keychain boundary.
// Deliberately discard bridge results so page JavaScript never acquires it.
export async function claimDevice(origin, payload, deviceLabel, bridge) {
  await nativeAuth(bridge).claim(origin, payload, deviceLabel);
}

export async function authorizeDevice(origin, bridge) {
  await nativeAuth(bridge).authorize(origin);
}
