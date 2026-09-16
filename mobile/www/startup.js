const OPENING_DELAY_MS = 700;

// Decide which local screen is allowed to appear while the saved device
// credential is exchanged for a browser session. Keeping this separate from
// the DOM makes the launch sequence testable without pretending to be WebKit.
export async function startApp(knownServer, actions) {
  if (!knownServer) {
    actions.showPairing();
    return;
  }

  const scheduleOpening = actions.scheduleOpening
    || ((callback) => setTimeout(callback, OPENING_DELAY_MS));
  const cancelOpening = actions.cancelOpening || clearTimeout;
  const openingTimer = scheduleOpening(actions.showOpening);

  try {
    await actions.authorizeDevice(knownServer);
    await actions.bindNativeServer(knownServer);
    cancelOpening(openingTimer);
    actions.navigate(knownServer);
  } catch (authError) {
    cancelOpening(openingTimer);
    if (authError?.code === "not_paired") {
      actions.forgetServer();
      try { await actions.clearNativeServer(); } catch { /* already unbound is fine */ }
      actions.showPairing("This device is no longer paired. Create a new code to pair it again.");
      return;
    }

    actions.showConnectionError(
      "Could not reach your paired moa. Check the connection and reopen the app.",
    );
  }
}
