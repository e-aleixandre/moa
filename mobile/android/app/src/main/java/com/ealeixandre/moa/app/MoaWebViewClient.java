package com.ealeixandre.moa.app;

import android.content.ActivityNotFoundException;
import android.content.Context;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.Uri;
import android.os.Handler;
import android.os.Looper;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import com.getcapacitor.Bridge;
import com.getcapacitor.BridgeWebViewClient;

// Capacitor's client plus moa's navigation boundary and recovery (iOS
// PairedServerNavigationPlugin + PairedServerRecoveryController).
//
// The main frame may show the local pairing page or the paired origin; every
// other URL opens in the system browser. Capacitor's own allowNavigation is a
// build-time host list and cannot name an origin paired at runtime.
//
// A transport failure while loading the paired page is retried with backoff
// and again when the network returns or the app comes back. HTTP errors are
// answers, not outages, and are never retried. A path change alone never
// reloads a live page: that would discard an unsent composer draft.
final class MoaWebViewClient extends BridgeWebViewClient {

    private final Bridge bridge;
    private final DeviceCredentialStore credentials;
    private final Runnable beforeNavigation;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final TransportRetry retry = new TransportRetry(new TransportRetry.Host() {
        @Override
        public void postDelayed(Runnable task, long delayMillis) {
            main.postDelayed(task, delayMillis);
        }

        @Override
        public void removeCallbacks(Runnable task) {
            main.removeCallbacks(task);
        }

        @Override
        public boolean reload(String url) {
            WebView view = bridge.getWebView();
            if (view == null || !OriginPolicy.matchesPaired(url, credentials.pairedOrigin())) return false;
            view.loadUrl(url);
            return true;
        }
    });
    // WebView reports a failed load as an error followed by onPageFinished
    // for its error page; that "finish" is not a loaded paired page.
    private boolean loadFailed;

    MoaWebViewClient(Bridge bridge, DeviceCredentialStore credentials, Runnable beforeNavigation) {
        super(bridge);
        this.bridge = bridge;
        this.credentials = credentials;
        this.beforeNavigation = beforeNavigation;
        ConnectivityManager connectivity = bridge.getContext().getSystemService(ConnectivityManager.class);
        if (connectivity != null) {
            connectivity.registerDefaultNetworkCallback(new ConnectivityManager.NetworkCallback() {
                @Override
                public void onAvailable(Network network) {
                    main.post(() -> retry.now(true));
                }
            });
        }
    }

    @Override
    public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
        // Subframes stay where they are: they never get the native bridges
        // (main frame only), and opening every embedded page in a browser
        // would break previews.
        if (!request.isForMainFrame()) return false;
        String url = request.getUrl().toString();
        switch (OriginPolicy.navigation(url, credentials.pairedOrigin())) {
            case WEBVIEW:
                // Pairing changes (claim, reset) take effect on the bridges
                // here, as the page leaves: changing WebMessageListeners under
                // a live page breaks its existing port (the next message is
                // dropped, the one after crashes WebView).
                beforeNavigation.run();
                return false;
            case LOCAL_PAGE:
                beforeNavigation.run();
                loadLocalPage(view, OriginPolicy.localPageFor(url));
                return true;
            case DEFAULT:
                return super.shouldOverrideUrlLoading(view, request);
            default:
                openExternally(bridge.getContext(), request.getUrl());
                return true;
        }
    }

    // Every load of the local (pairing) page the app makes itself goes
    // through here, so a pending retry of the paired page cannot replace it.
    static void loadLocalPage(WebView view, String url) {
        WebViewClient client = view.getWebViewClient();
        if (client instanceof MoaWebViewClient) ((MoaWebViewClient) client).retry.navigating(url);
        view.loadUrl(url);
    }

    static void openExternally(Context context, Uri uri) {
        Intent intent = new Intent(Intent.ACTION_VIEW, uri)
            .addCategory(Intent.CATEGORY_BROWSABLE)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        try {
            context.startActivity(intent);
        } catch (ActivityNotFoundException ignored) {
            // Nothing can open it; staying put is the safe outcome.
        }
    }

    @Override
    public void onPageStarted(WebView view, String url, android.graphics.Bitmap favicon) {
        super.onPageStarted(view, url, favicon);
        loadFailed = false;
    }

    @Override
    public void onPageFinished(WebView view, String url) {
        super.onPageFinished(view, url);
        if (loadFailed) return;
        String paired = credentials.pairedOrigin();
        if (OriginPolicy.matchesPaired(url, paired)) {
            retry.stop();
            PushNavigator.get().navigationFinished(url, paired);
        } else if (retry.url() != null && !OriginPolicy.matchesPaired(retry.url(), paired)) {
            retry.stop();
        }
    }

    @Override
    public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
        super.onReceivedError(view, request, error);
        if (request.isForMainFrame()) loadFailed = true;
        if (!request.isForMainFrame() || !isTransportError(error.getErrorCode())) return;
        String url = request.getUrl().toString();
        if (!OriginPolicy.matchesPaired(url, credentials.pairedOrigin())) return;
        retry.failed(url);
    }

    static boolean isTransportError(int code) {
        switch (code) {
            case WebViewClient.ERROR_HOST_LOOKUP:
            case WebViewClient.ERROR_CONNECT:
            case WebViewClient.ERROR_TIMEOUT:
            case WebViewClient.ERROR_IO:
                return true;
            default:
                return false;
        }
    }

    // From the activity's onResume: a failed load is retried at once.
    void onForeground() {
        retry.now(true);
    }
}
