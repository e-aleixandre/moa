package com.ealeixandre.moa.app;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.View;
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
    private WeakReference<View> cover = new WeakReference<>(null);
    private String pendingOrigin;
    private String pendingUrl;
    private boolean pendingPairing;
    private volatile String visibleSession;
    private volatile boolean foreground;

    static PushNavigator get() {
        return SHARED;
    }

    private PushNavigator() {}

    void setWebView(WebView view) {
        main.post(() -> {
            webView = new WeakReference<>(view);
            if (pendingPairing && view != null) {
                pendingPairing = false;
                view.loadUrl(PAIRING_URL);
            }
        });
    }

    // UI thread, before the activity can deliver a tap.
    void setCover(View view) {
        cover = new WeakReference<>(view);
    }

    // UI thread. Lifted only once a local page has loaded and WebView has
    // drawn it: uncovering at commit showed one more frame of the old page.
    void localPageFinished(WebView view, String url) {
        View current = cover.get();
        if (current == null || current.getVisibility() != View.VISIBLE || !OriginPolicy.isLocal(url)) return;
        view.postVisualStateCallback(0, new WebView.VisualStateCallback() {
            @Override
            public void onComplete(long requestId) {
                if (OriginPolicy.isLocal(view.getUrl())) setCovered(false);
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

    // UI thread; the authentication runs in PushTaps, off it.
    void handleTap(Context context, Map<String, String> envelope) {
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
            public boolean knownExpired() {
                return credentials.knownExpired();
            }

            @Override
            public boolean credentialExpired() {
                try {
                    DeviceCredentialStore.Stored stored = credentials.load();
                    return stored != null && stored.expiresAtMillis <= System.currentTimeMillis();
                } catch (DeviceCredentialStore.StoreException e) {
                    return false;
                }
            }

            @Override
            public String pairedOrigin() {
                return credentials.pairedOrigin();
            }
        }, new PushTaps.Navigation() {
            @Override
            public void open(PushEnvelope.Destination destination, String pairedOrigin) {
                PushNavigator.this.open(destination, pairedOrigin);
            }

            @Override
            public void pair() {
                showPairing();
            }

            @Override
            public void cover() {
                setCovered(true);
            }

            @Override
            public void uncover() {
                setCovered(false);
            }
        });
    }

    private void setCovered(boolean covered) {
        View view = cover.get();
        if (view != null) view.setVisibility(covered ? View.VISIBLE : View.GONE);
    }

    // UI thread. An expired credential: whatever is on screen, the WebView
    // goes to the local pairing page, which forgets the server like an
    // unpair does, and a held deep link is dropped. The credential itself
    // stays, as on a launch with an expired credential; authorize refuses it
    // (DeviceAuthBridge), so the page cannot resume into the paired origin.
    // On a cold start the WebView may not be registered yet: load it then.
    private void showPairing() {
        pendingUrl = null;
        pendingOrigin = null;
        WebView view = webView.get();
        if (view == null) {
            pendingPairing = true;
            return;
        }
        view.loadUrl(PAIRING_URL);
    }

    // UI thread. A cold-start tap is held until the paired page has loaded,
    // which the local page only allows after `authorize` succeeded.
    void navigationFinished(String url, String pairedOrigin) {
        WebView view = webView.get();
        if (pendingUrl == null || view == null) return;
        if (!pendingOrigin.equals(pairedOrigin) || !OriginPolicy.matchesPaired(url, pairedOrigin)) return;
        String next = pendingUrl;
        pendingUrl = null;
        pendingOrigin = null;
        view.loadUrl(next);
    }

    void reset() {
        visibleSession = null;
        main.post(() -> {
            pendingUrl = null;
            pendingOrigin = null;
        });
    }

    private void open(PushEnvelope.Destination destination, String pairedOrigin) {
        String origin = OriginPolicy.normalizeOrigin(pairedOrigin);
        String url = destination.url(origin);
        if (origin == null || url == null || !OriginPolicy.matchesPaired(url, origin)) return;
        WebView view = webView.get();
        if (view == null || view.getProgress() < 100 || !OriginPolicy.matchesPaired(view.getUrl(), origin)) {
            // Not showing the paired page yet (cold start, pairing, loading):
            // hold the deep link until it has loaded.
            pendingOrigin = origin;
            pendingUrl = url;
            return;
        }
        if ("home".equals(destination.kind)) return; // the paired page is already on screen
        // The paired page is loaded: open the destination in place so nothing
        // being typed is lost. Load the deep link only if the page cannot (an
        // older frontend, a session it does not know).
        view.evaluateJavascript(PushMessages.openInPlaceScript(destination, url), (String result) -> {
            if ("true".equals(result)) return;
            WebView current = webView.get();
            if (current != null && OriginPolicy.matchesPaired(current.getUrl(), origin)) current.loadUrl(url);
        });
    }
}
