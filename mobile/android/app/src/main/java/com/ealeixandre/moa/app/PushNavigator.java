package com.ealeixandre.moa.app;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.webkit.WebView;
import java.lang.ref.WeakReference;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.Executors;

// What a delivered notification does in the app (iOS PushNavigator).
// Destinations always come from re-authenticating the envelope with the
// current key, never from fields the relay could set next to it (iOS review
// M2): the tap intent carries only the opaque envelope.
final class PushNavigator {

    static final String EXTRA_V = "com.ealeixandre.moa.app.push.v";
    static final String EXTRA_K = "com.ealeixandre.moa.app.push.k";
    static final String EXTRA_C = "com.ealeixandre.moa.app.push.c";
    // The local page's "back to pairing" entry, as after an unpair (boot.js).
    static final String PAIRING_URL = OriginPolicy.LOCAL_ORIGIN + "/?unpaired=1";

    private static final PushNavigator SHARED = new PushNavigator();

    private final Handler main = new Handler(Looper.getMainLooper());
    private final PushTaps taps = new PushTaps(Executors.newSingleThreadExecutor(), main::post);
    private WeakReference<WebView> webView = new WeakReference<>(null);
    private NavigationGate gate;
    private String pendingUrl;
    private int pendingGeneration;
    // The last destination opened in place, until the page says whether it
    // could show it (openFallback). Only the latest tap's token counts.
    private int fallbackToken;
    private String fallbackUrl;
    private String fallbackOrigin;
    private int fallbackGeneration;
    private boolean pendingPairing;
    private volatile String visibleSession;
    private volatile boolean foreground;

    static PushNavigator get() {
        return SHARED;
    }

    private PushNavigator() {}

    // UI thread, from MainActivity.load.
    void setWebView(WebView view, Context context) {
        gateFor(context);
        main.post(() -> {
            webView = new WeakReference<>(view);
            if (pendingPairing && view != null) {
                pendingPairing = false;
                MoaWebViewClient.loadLocalPage(view, PAIRING_URL);
            }
        });
    }

    void setForeground(boolean value) {
        foreground = value;
    }

    boolean isForeground() {
        return foreground;
    }

    void setVisibleSession(String id) {
        visibleSession = PushEnvelope.isSessionId(id) ? id : null;
    }

    String visibleSession() {
        return visibleSession;
    }

    static Map<String, String> envelopeFrom(Intent intent) {
        if (intent == null) return null;
        Bundle extras = intent.getExtras();
        if (extras == null || !extras.containsKey(EXTRA_C)) return null;
        Map<String, String> envelope = new HashMap<>();
        envelope.put("v", extras.getString(EXTRA_V));
        envelope.put("k", extras.getString(EXTRA_K));
        envelope.put("c", extras.getString(EXTRA_C));
        return envelope;
    }

    // UI thread. Both are per-process singletons, so the first caller's
    // context builds the gate for every later one.
    private void gateFor(Context context) {
        if (gate != null) return;
        NativePushRegistration registration = NativePushRegistration.get(context);
        DeviceCredentialStore credentials = DeviceCredentialStore.get(context);
        gate = new NavigationGate(new NavigationGate.State() {
            @Override
            public boolean knownValid() {
                return credentials.knownValid();
            }

            @Override
            public String knownOrigin() {
                return credentials.knownOrigin();
            }

            @Override
            public int generation() {
                return registration.generation();
            }
        });
    }

    // UI thread. The one check every private navigation the app starts
    // passes right before it (NavigationGate). `generation` is the push
    // generation the navigation was decided in.
    boolean admit(String url, int generation) {
        return gate != null && gate.admit(url, generation, this::showPairing);
    }

    // UI thread. A reload of a page already on the paired origin (transport
    // retry): not decided in any push generation, so only the credential
    // and the origin are fenced.
    boolean admitReload(String url) {
        return gate != null && gate.admit(url, gate.generation(), this::showPairing);
    }

    // UI thread; the authentication runs in PushTaps, off it.
    void handleTap(Context context, Map<String, String> envelope) {
        gateFor(context);
        NativePushRegistration registration = NativePushRegistration.get(context);
        DeviceCredentialStore credentials = DeviceCredentialStore.get(context);
        taps.handle(envelope, new PushTaps.Source() {
            @Override
            public int generation() {
                return registration.generation();
            }

            @Override
            public PushEnvelope.Keys currentKeys() {
                return registration.currentKeys();
            }

            @Override
            public boolean credentialExpired() throws PushTaps.Unreadable {
                try {
                    DeviceCredentialStore.Stored stored = credentials.load();
                    return stored != null && stored.expiresAtMillis <= System.currentTimeMillis();
                } catch (DeviceCredentialStore.StoreException e) {
                    throw new PushTaps.Unreadable();
                }
            }

            @Override
            public String pairedOrigin() {
                return credentials.pairedOrigin();
            }

            @Override
            public boolean knownValid() {
                return credentials.knownValid();
            }
        }, new PushTaps.Navigation() {
            @Override
            public void open(PushEnvelope.Destination destination, String pairedOrigin, int generation) {
                PushNavigator.this.open(destination, pairedOrigin, generation);
            }

            @Override
            public void pair() {
                showPairing();
            }
        });
    }

    // UI thread. An expired credential: whatever is on screen, the WebView
    // goes to the local pairing page, which forgets the server like an
    // unpair does, and a held deep link is dropped. The credential itself
    // stays, as on a launch with an expired credential; authorize refuses it
    // (DeviceAuthBridge), so the page cannot resume into the paired origin.
    // On a cold start the WebView may not be registered yet: load it then.
    private void showPairing() {
        pendingUrl = null;
        WebView view = webView.get();
        if (view == null) {
            pendingPairing = true;
            return;
        }
        MoaWebViewClient.loadLocalPage(view, PAIRING_URL);
    }

    // UI thread, once a paired page has loaded. A cold-start tap is held
    // until then, which the local page only allows after `authorize`
    // succeeded.
    void navigationFinished() {
        WebView view = webView.get();
        if (pendingUrl == null || view == null) return;
        String next = pendingUrl;
        pendingUrl = null;
        if (admit(next, pendingGeneration)) view.loadUrl(next);
    }

    void reset() {
        visibleSession = null;
        main.post(() -> pendingUrl = null);
    }

    private void open(PushEnvelope.Destination destination, String pairedOrigin, int generation) {
        String url = destination.url(pairedOrigin);
        if (!admit(url, generation)) return;
        WebView view = webView.get();
        if (view == null || view.getProgress() < 100 || !OriginPolicy.matchesPaired(view.getUrl(), pairedOrigin)) {
            // Not showing the paired page yet (cold start, pairing, loading):
            // hold the deep link until it has loaded.
            pendingUrl = url;
            pendingGeneration = generation;
            return;
        }
        if ("home".equals(destination.kind)) return; // the paired page is already on screen
        // The paired page is loaded: open the destination in place so nothing
        // being typed is lost. Load the deep link only if the page cannot (an
        // older frontend, a session it does not know).
        // The gate above ran just now, on this thread: nothing changed since.
        int token = ++fallbackToken;
        fallbackUrl = url;
        fallbackOrigin = pairedOrigin;
        fallbackGeneration = generation;
        view.evaluateJavascript(PushMessages.openInPlaceScript(destination, token), (String result) -> {
            if (!"true".equals(result)) openFallback(token);
        });
    }

    // UI thread: the page could not show the destination opened in place,
    // answered either through the evaluateJavascript result or, after a
    // Promise, through the push port (NativePushBridge). Loads it instead,
    // once, if the page is still the paired one (not pairing, say) and the
    // gate admits it for the tap's own generation.
    void openFallback(int token) {
        if (token != fallbackToken || fallbackUrl == null) return;
        String url = fallbackUrl;
        fallbackUrl = null;
        WebView view = webView.get();
        if (view == null || !OriginPolicy.matchesPaired(view.getUrl(), fallbackOrigin)) return;
        if (admit(url, fallbackGeneration)) view.loadUrl(url);
    }
}
