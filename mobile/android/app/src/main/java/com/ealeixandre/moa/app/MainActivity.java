package com.ealeixandre.moa.app;

import android.Manifest;
import android.content.Intent;
import android.os.Build;
import android.os.Bundle;
import android.webkit.WebView;
import androidx.activity.OnBackPressedCallback;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import com.getcapacitor.BridgeActivity;
import java.util.Map;
import java.util.concurrent.CompletableFuture;

// The Capacitor activity plus everything iOS does in MoaBridgeViewController,
// AppDelegate and SceneDelegate. Share-in (ACTION_SEND) is ShareActivity's;
// what it saves for later reaches the page through ShareInboxBridge.
public class MainActivity extends BridgeActivity {

    private DeviceCredentialStore credentials;
    private WebBridges bridges;
    private MoaWebViewClient webViewClient;
    private CompletableFuture<Boolean> permissionResult;

    // Registered at construction: an ActivityResult launcher must exist
    // before the activity starts.
    private final ActivityResultLauncher<String> notificationPermission = registerForActivityResult(
        new ActivityResultContracts.RequestPermission(),
        (Boolean granted) -> {
            CompletableFuture<Boolean> result = permissionResult;
            permissionResult = null;
            if (result != null) result.complete(Boolean.TRUE.equals(granted));
        }
    );

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        credentials = DeviceCredentialStore.get(this);
        super.onCreate(savedInstanceState);
        PushNotifier.ensureChannels(this);
        NativePushRegistration.get(this).setPermissionRequester(this::requestNotificationPermission);

        // Back: the page closes its sheet or steps back (window.MoaHandleBack);
        // at the root, or on a page without a handler, the app is minimized,
        // never closed. Through the dispatcher, so predictive back works.
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                WebView webView = bridge == null ? null : bridge.getWebView();
                if (webView == null) {
                    moveTaskToBack(true);
                    return;
                }
                webView.evaluateJavascript(BackDecision.SCRIPT, (String result) -> {
                    if (BackDecision.shouldMinimize(result)) moveTaskToBack(true);
                });
            }
        });
    }

    // BridgeActivity.onCreate calls load(), which builds the bridge and starts
    // loading the local page. The native bridges have to exist before that
    // first document starts, so they go on the layout's WebView first.
    @Override
    protected void load() {
        WebView webView = findViewById(com.getcapacitor.android.R.id.webview);
        if (webView != null) {
            DeviceAuthBridge auth = new DeviceAuthBridge(this, credentials);
            bridges = new WebBridges(webView, credentials, auth, new NativePushBridge(this),
                new ShareInboxBridge(this, credentials));
            bridges.install();
        }
        super.load();
        if (bridge == null) return;
        WebView bridgeView = bridge.getWebView();
        // Capacitor's CapacitorCookies and CapacitorHttp plugins expose
        // addJavascriptInterface objects to every origin and frame (a cookie
        // writer among them). moa uses neither; removing them before any
        // remote page loads keeps them off the paired server's pages.
        bridgeView.removeJavascriptInterface("CapacitorCookiesAndroidInterface");
        bridgeView.removeJavascriptInterface("CapacitorHttpAndroidInterface");
        webViewClient = new MoaWebViewClient(bridge, credentials, bridges == null ? () -> { } : bridges::syncPairedOrigin);
        bridge.setWebViewClient(webViewClient);
        bridgeView.setWebChromeClient(new MoaWebChromeClient(bridge, credentials));
        PushNavigator.get().setWebView(bridgeView);
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        Map<String, String> envelope = PushNavigator.envelopeFrom(intent);
        if (envelope != null) {
            // Consumed once: a recreated activity must not replay the tap.
            intent.removeExtra(PushNavigator.EXTRA_C);
            PushNavigator.get().handleTap(this, envelope);
        }
    }

    @Override
    public void onResume() {
        super.onResume();
        PushNavigator.get().setForeground(true);
        NativePushRegistration.get(this).refreshIfNeeded(false);
        if (webViewClient != null) webViewClient.onForeground();
    }

    @Override
    public void onPause() {
        PushNavigator.get().setForeground(false);
        super.onPause();
    }

    private CompletableFuture<Boolean> requestNotificationPermission() {
        CompletableFuture<Boolean> result = new CompletableFuture<>();
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            result.complete(true);
            return result;
        }
        runOnUiThread(() -> {
            if (permissionResult != null) permissionResult.complete(false);
            permissionResult = result;
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS);
        });
        return result;
    }
}
