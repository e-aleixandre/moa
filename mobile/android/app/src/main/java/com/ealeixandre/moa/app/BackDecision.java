package com.ealeixandre.moa.app;

// The Back button (owner's rule): close the open sheet or panel; if none, go
// back inside the app; at the root, minimize -- never close. The page owns
// the first two (window.MoaHandleBack, a synchronous boolean); the shell
// only learns whether the page handled it.
public final class BackDecision {

    // Evaluated in the page on every Back. Anything but a literal true --
    // no handler (the local pairing page, an older frontend), an exception,
    // a promise -- means the page did not handle it.
    public static final String SCRIPT =
        "(() => { try { return !!(window.MoaHandleBack && window.MoaHandleBack() === true); }"
            + " catch (e) { return false; } })()";

    private BackDecision() {}

    // `result` is WebView.evaluateJavascript's JSON-encoded value.
    public static boolean shouldMinimize(String result) {
        return !"true".equals(result);
    }
}
