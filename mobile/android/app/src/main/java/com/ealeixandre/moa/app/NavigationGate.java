package com.ealeixandre.moa.app;

// The last fence before the app itself sends the WebView to a page of the
// paired origin: every loadUrl of a private URL, and every destination
// handed to the page's window.MoaNavigation, asks here first, on the UI
// thread, right before doing it. Memory only: no store IO on that thread.
//
// An expired credential goes to pairing, as an expired tap does. Without a
// paired origin (unpaired) nothing happens: the page is already the local
// one, and reloading it would interrupt a pairing in progress. A valid
// credential with a navigation
// decided for another pairing (the origin changed, or a forget ended the
// push generation it was decided in) goes nowhere: the paired page on screen
// stays, which is home; reloading it would discard an unsent draft.
//
// Not a replacement for authenticating in the background: it only refuses.
// Pure Java so the JVM tests can drive it.
final class NavigationGate {

    interface State {
        // DeviceCredentialStore.knownValid / knownOrigin, from memory.
        boolean knownValid();

        String knownOrigin();

        // NativePushRegistration.generation: moves only when push forgets.
        int generation();
    }

    private final State state;

    NavigationGate(State state) {
        this.state = state;
    }

    int generation() {
        return state.generation();
    }

    // Whether url, decided in `generation`, may be navigated to now. When
    // the paired credential has expired, runs `pair` and refuses.
    boolean admit(String url, int generation, Runnable pair) {
        if (!state.knownValid()) {
            if (state.knownOrigin() != null) pair.run();
            return false;
        }
        if (state.generation() != generation || !OriginPolicy.matchesPaired(url, state.knownOrigin())) return false;
        return true;
    }
}
