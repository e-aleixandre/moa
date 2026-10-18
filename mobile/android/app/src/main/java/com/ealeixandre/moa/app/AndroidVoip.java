package com.ealeixandre.moa.app;

import android.content.Context;
import android.content.SharedPreferences;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.messaging.FirebaseMessaging;
import java.io.IOException;
import java.util.Map;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

// The incoming-call destination on this phone: its secret, its registration
// and the FCM messages that ring it. The platform half of VoipRegistration
// and of the call envelope, as NativePushRegistration is for alerts.
final class AndroidVoip {

    private static final String PREFS = "moa_voip";
    private static final long TOKEN_TIMEOUT_SECONDS = 20;
    private static final int TIMEOUT_MILLIS = 15_000;

    private static AndroidVoip shared;

    private final Context context;
    private final DeviceCredentialStore credentials;
    private final CallSecretStore secrets;
    private final VoipRegistration registration;
    // One registration at a time and in order: the DELETE of a forgotten one
    // lands after the POST it is cleaning up after.
    private final ExecutorService operations = Executors.newSingleThreadExecutor();

    static synchronized AndroidVoip get(Context context) {
        if (shared == null) shared = new AndroidVoip(context.getApplicationContext());
        return shared;
    }

    private AndroidVoip(Context context) {
        this.context = context;
        this.credentials = DeviceCredentialStore.get(context);
        this.secrets = new CallSecretStore(new KeystoreBox(context, "moa-voip-secret-v1", "voip-secret.bin"));
        this.registration = new VoipRegistration(new HostEdge(), secrets, new PrefsMemory(
            context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)));
    }

    // On every foreground and on a new FCM token. Does nothing unless the app
    // is on screen, paired, and the server knows incoming calls.
    void refresh(boolean force) {
        operations.execute(() -> registration.refresh(force));
    }

    // Any registration challenge: only the call registration that is waiting
    // for it answers.
    void handleChallenge(String challenge) {
        registration.handleChallenge(challenge);
    }

    // True when the message is for the call destination, whether or not it
    // rings: a call envelope must never fall through to the alert path and
    // show as a generic alert.
    boolean receive(Map<String, String> data) {
        CallSecretStore.Secret secret = secrets.load();
        PushEnvelope.Keys keys = secret == null ? null : PushEnvelope.deriveKeys(secret.secret);
        if (!PushMessages.addressedTo(data, keys)) return false;
        DeviceCredentialStore.Stored paired;
        try {
            paired = credentials.load();
        } catch (DeviceCredentialStore.StoreException e) {
            return true;
        }
        // The secret belongs to one pairing: after an unpair, a re-pair or
        // an expiry nothing rings with it.
        if (paired == null || paired.expiresAtMillis <= System.currentTimeMillis() || !paired.origin.equals(secret.origin)
            || !secret.deviceId.equals(VoiceAuth.deviceId(paired.credential)) || !secret.origin.startsWith("https://")) {
            return true;
        }
        CallInvite invite = CallInvite.fromPush(data, keys, System.currentTimeMillis() / 1000);
        String authorization = secret.authorization();
        if (invite != null && authorization != null) NativeCalls.ring(context, invite, secret.origin, authorization);
        return true;
    }

    // Unpaired, paired again or revoked: the secret goes and anything ringing
    // with it. `paired` is the credential that can still tell the server, or
    // null when it was refused or is already gone.
    static void forget(Context context, DeviceCredentialStore.Stored paired) {
        AndroidVoip voip = get(context);
        NativeCalls.forgetRinging();
        voip.registration.forgetLocal();
        if (paired != null) voip.operations.execute(() -> voip.registration.unregister(paired));
    }

    private final class HostEdge implements VoipRegistration.Host {
        @Override
        public boolean foreground() {
            return PushNavigator.get().isForeground();
        }

        @Override
        public DeviceCredentialStore.Stored paired() {
            try {
                DeviceCredentialStore.Stored stored = credentials.load();
                return stored != null && stored.expiresAtMillis > System.currentTimeMillis() ? stored : null;
            } catch (DeviceCredentialStore.StoreException e) {
                return null;
            }
        }

        @Override
        public String relayUrl() {
            String relay = BuildConfig.MOA_PUSH_RELAY_URL;
            return OriginPolicy.validRelayUrl(relay) ? relay : null;
        }

        // The alerts' token: an app has one FCM token and two destinations on it.
        @Override
        public String fcmToken() throws IOException {
            try {
                if (FirebaseApp.getApps(context).isEmpty()) throw new IOException("unsupported");
                FirebaseMessaging messaging = FirebaseMessaging.getInstance();
                // Auto-init is off in the manifest so nothing talks to Firebase
                // before this phone can be rung or alerted; registering the call
                // destination is what turns it on, as iOS registers PushKit.
                messaging.setAutoInitEnabled(true);
                return Tasks.await(messaging.getToken(), TOKEN_TIMEOUT_SECONDS, TimeUnit.SECONDS);
            } catch (ExecutionException | TimeoutException | RuntimeException e) {
                throw new IOException("no token");
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                throw new IOException("interrupted");
            }
        }

        @Override
        public long now() {
            return System.currentTimeMillis();
        }

        @Override
        public NativeHttp.Response send(String method, String url, Map<String, String> headers, String body) throws IOException {
            return NativeHttp.send(method, url, headers, body, TIMEOUT_MILLIS);
        }
    }

    private static final class PrefsMemory implements VoipRegistration.Memory {
        private final SharedPreferences prefs;

        PrefsMemory(SharedPreferences prefs) {
            this.prefs = prefs;
        }

        @Override
        public String get(String key) {
            return prefs.getString(key, null);
        }

        @Override
        public void put(String key, String value) {
            prefs.edit().putString(key, value).apply();
        }

        @Override
        public void clear() {
            prefs.edit().clear().apply();
        }
    }
}
