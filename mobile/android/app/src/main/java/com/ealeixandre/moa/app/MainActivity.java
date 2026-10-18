package com.ealeixandre.moa.app;

import android.Manifest;
import android.app.NotificationManager;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.webkit.WebView;
import androidx.activity.OnBackPressedCallback;
import androidx.appcompat.app.AlertDialog;
import androidx.core.app.NotificationManagerCompat;
import androidx.core.content.ContextCompat;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import com.getcapacitor.BridgeActivity;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.function.Consumer;

// The Capacitor activity plus everything iOS does in MoaBridgeViewController,
// AppDelegate and SceneDelegate. Share-in (ACTION_SEND) is ShareActivity's;
// what it saves for later reaches the page through ShareInboxBridge.
public class MainActivity extends BridgeActivity {

    private DeviceCredentialStore credentials;
    private WebBridges bridges;
    private MoaWebViewClient webViewClient;
    private CompletableFuture<Boolean> permissionResult;
    private Consumer<Boolean> microphoneAnswer;

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

    // A call that needs the microphone asks here, with the app on screen.
    private final ActivityResultLauncher<String> microphonePermission = registerForActivityResult(
        new ActivityResultContracts.RequestPermission(),
        (Boolean granted) -> {
            Consumer<Boolean> answer = microphoneAnswer;
            microphoneAnswer = null;
            if (answer != null) answer.accept(Boolean.TRUE.equals(granted));
        }
    );

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        credentials = DeviceCredentialStore.get(this);
        // Before super.onCreate, which builds the bridge with its plugins.
        registerPlugin(QRScannerPlugin.class);
        super.onCreate(savedInstanceState);
        PushNotifier.ensureChannels(this);
        NativePushRegistration.get(this).setPermissionRequester(this::requestNotificationPermission);
        NativeCalls.get(this).setMicRequester(this::requestMicrophone);

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
        // A cold start from the missed-call notification; a restored activity
        // has already used its intent.
        if (savedInstanceState == null) openMissedCall(getIntent());
    }

    // BridgeActivity.onCreate calls load(), which builds the bridge and starts
    // loading the local page. The native bridges have to exist before that
    // first document starts, so they go on the layout's WebView first.
    @Override
    protected void load() {
        WebView webView = findViewById(com.getcapacitor.android.R.id.webview);
        if (webView != null) {
            DeviceAuthBridge auth = new DeviceAuthBridge(this, credentials, this::pairingCompleted);
            bridges = new WebBridges(webView, credentials, auth, new NativePushBridge(this),
                new ShareInboxBridge(this, credentials), NativeCalls.get(this).bridge());
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
        PushNavigator.get().setWebView(bridgeView, this);
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
        openMissedCall(intent);
    }

    // The missed-call notification's tap, once: a recreated activity must not
    // replay it.
    private void openMissedCall(Intent intent) {
        if (intent == null) return;
        String session = PushMessages.missedCallSession(intent.getAction(), intent.getStringExtra(IncomingCallNotifier.EXTRA_SESSION));
        if (session == null) return;
        intent.setAction(null);
        PushNavigator.get().handleMissedCall(this, session);
    }

    // A pairing finished inside the running activity: what onResume does for
    // an already paired phone is due now, not at the next resume. Any thread.
    void pairingCompleted() {
        runOnUiThread(() -> {
            AndroidVoip.get(this).refresh(false);
            askCallPermissions();
        });
    }

    @Override
    public void onResume() {
        super.onResume();
        PushNavigator.get().setForeground(true);
        NativePushRegistration.get(this).refreshIfNeeded(false);
        AndroidVoip.get(this).refresh(false);
        askCallPermissions();
        if (webViewClient != null) webViewClient.onForeground();
    }

    @Override
    public void onPause() {
        PushNavigator.get().setForeground(false);
        super.onPause();
    }

    @Override
    public void onDestroy() {
        // The call outlives the activity; it must not hold on to it.
        NativeCalls.get(this).setMicRequester(null);
        super.onDestroy();
    }

    // Once, the first time a paired phone is opened with something missing:
    // a locked phone cannot ask when a call comes in. What was asked is not
    // asked again at every launch; the settings stay where they are.
    private void askCallPermissions() {
        if (credentials.pairedOrigin() == null || !credentials.knownValid()) return;
        SharedPreferences prefs = getSharedPreferences("moa_call_permissions", MODE_PRIVATE);
        List<CallPermissions.Step> missing = CallPermissions.missing(Build.VERSION.SDK_INT,
            ContextCompat.checkSelfPermission(this, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED,
            NotificationManagerCompat.from(this).areNotificationsEnabled(), canUseFullScreen());
        if (!CallPermissions.shouldAsk(true, prefs.getBoolean("asked", false), missing)) return;
        prefs.edit().putBoolean("asked", true).apply();
        new AlertDialog.Builder(this)
            .setMessage(R.string.calls_permission_message)
            .setPositiveButton(R.string.calls_permission_continue, (dialog, which) -> askCallStep(missing, 0))
            .setNegativeButton(R.string.calls_permission_not_now, null)
            .show();
    }

    private boolean canUseFullScreen() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) return true;
        NotificationManager manager = getSystemService(NotificationManager.class);
        return manager == null || manager.canUseFullScreenIntent();
    }

    private void askCallStep(List<CallPermissions.Step> steps, int index) {
        if (index >= steps.size()) return;
        Runnable next = () -> runOnUiThread(() -> askCallStep(steps, index + 1));
        switch (steps.get(index)) {
            case MICROPHONE:
                requestMicrophone(granted -> next.run());
                break;
            case NOTIFICATIONS:
                requestNotificationPermission().thenAccept(granted -> next.run());
                break;
            default:
                // The last step: the system's own screen for this app.
                startActivity(new Intent(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT,
                    Uri.parse("package:" + getPackageName())));
        }
    }

    private void requestMicrophone(Consumer<Boolean> answer) {
        runOnUiThread(() -> {
            if (microphoneAnswer != null) microphoneAnswer.accept(false);
            microphoneAnswer = answer;
            microphonePermission.launch(Manifest.permission.RECORD_AUDIO);
        });
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
