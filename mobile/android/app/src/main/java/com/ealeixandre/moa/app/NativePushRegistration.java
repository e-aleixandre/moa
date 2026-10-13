package com.ealeixandre.moa.app;

import android.Manifest;
import android.content.Context;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.os.Build;
import android.util.Log;
import androidx.core.app.NotificationManagerCompat;
import androidx.core.content.ContextCompat;
import com.google.android.gms.tasks.Tasks;
import com.google.firebase.FirebaseApp;
import com.google.firebase.messaging.FirebaseMessaging;
import java.io.IOException;
import java.security.GeneralSecurityException;
import java.security.SecureRandom;
import java.util.HashMap;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import org.json.JSONException;
import org.json.JSONObject;

// Registers this device for native push (relay/PROTOCOL.md §1-2) over FCM and
// keeps the registration alive: the Android port of iOS
// NativePushRegistration.swift, with the same fences.
//
// - Every server/relay operation runs on one serial executor, so a DELETE
//   queued behind an in-flight POST lands after it (FIFO), and a generation
//   counter fences the operations a disable, reset or pairing change made
//   obsolete: they stop at their next ensureCurrent before saving anything.
// - A relay challenge is confirmed only for a registration this app started
//   in the last five minutes, for this token, with this K_send, and the
//   confirmation is accepted only if the relay says it is for that token, env
//   and provider (review I2).
// - The push secret S is stored (Keystore-encrypted, no backup) before
//   anything is registered, and deleting it is retried until it succeeds;
//   while a delete is pending, push is reported as on and nothing registers.
final class NativePushRegistration {

    static final class PushException extends Exception {
        final String code;

        PushException(String code) {
            super(code);
            this.code = code;
        }
    }

    private static final String TAG = "MoaPush";
    private static final String PREFS = "moa_native_push";
    private static final String STATE_ORIGIN = "state_origin";
    private static final String STATE_TOKEN = "state_token";
    private static final String STATE_ENV = "state_env";
    private static final String PENDING_DELETE = "pending_delete_origin";
    private static final String SECRET_DELETE_PENDING = "secret_delete_pending";
    private static final String PERMISSION_ASKED = "permission_asked";
    private static final long CHALLENGE_WINDOW_MILLIS = 300_000;
    private static final long TOKEN_TIMEOUT_SECONDS = 20;
    private static final long REFRESH_MARGIN_SECONDS = 30L * 24 * 3600;
    private static final long REFRESH_INTERVAL_MILLIS = 3_600_000;
    private static final int TIMEOUT_MILLIS = 15_000;

    interface PermissionRequester {
        // Asks for POST_NOTIFICATIONS (Android 13+) and completes with the answer.
        CompletableFuture<Boolean> request();
    }

    private static final class Pending {
        final String id = UUID.randomUUID().toString();
        final long startedAt = System.currentTimeMillis();
        final String relay;
        final String token;
        final byte[] sendKey;
        final CompletableFuture<RelayProtocol.Handle> result = new CompletableFuture<>();

        Pending(String relay, String token, byte[] sendKey) {
            this.relay = relay;
            this.token = token;
            this.sendKey = sendKey;
        }
    }

    private static final class ServerStatus {
        final boolean registered;
        final String relayUrl;
        final long expiresAt;
        final JSONObject last;

        ServerStatus(boolean registered, String relayUrl, long expiresAt, JSONObject last) {
            this.registered = registered;
            this.relayUrl = relayUrl;
            this.expiresAt = expiresAt;
            this.last = last;
        }
    }

    private static NativePushRegistration shared;

    private final Context context;
    private final DeviceCredentialStore credentials;
    private final KeystoreBox secretBox;
    private final SharedPreferences prefs;
    private final ExecutorService operations = Executors.newSingleThreadExecutor();
    private final ExecutorService io = Executors.newCachedThreadPool();
    // Held by "is this generation still current?" plus the local effects
    // that depend on it (S, STATE_*, posting a message), and by ending a
    // generation plus forgetting, so a forget cannot land between an
    // operation's check and its effects. Never held across network or token
    // waits.
    private final PushLifecycle lifecycle = new PushLifecycle();
    private volatile PermissionRequester permissionRequester;
    private Pending pending;
    private long lastRefresh;

    static synchronized NativePushRegistration get(Context context) {
        if (shared == null) shared = new NativePushRegistration(context.getApplicationContext());
        return shared;
    }

    private NativePushRegistration(Context context) {
        this.context = context;
        this.credentials = DeviceCredentialStore.get(context);
        this.secretBox = new KeystoreBox(context, "moa-push-secret-v1", "push-secret.bin");
        this.prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    void setPermissionRequester(PermissionRequester requester) {
        permissionRequester = requester;
    }

    // MARK: bridge entry points (called off the UI thread)

    // A pending DELETE (offline disable) is kept until a new POST succeeds in
    // this same generation; failing here must not abandon it.
    void enable() throws PushException {
        DeviceCredentialStore.Stored paired = pairedCredential();
        int current = lifecycle.generation();
        if (!firebaseAvailable()) throw new PushException("unsupported");
        if (!requestPermission()) throw new PushException("denied");
        ensureCurrent(current, paired);
        run(() -> {
            sync(paired, current, true);
            return null;
        });
    }

    // The local key goes first and unconditionally, so turning push off works
    // offline; the server is told best-effort and again on later launches.
    void disable() throws PushException {
        DeviceCredentialStore.Stored paired;
        try {
            paired = credentials.load();
        } catch (DeviceCredentialStore.StoreException e) {
            paired = null;
        }
        boolean removed = forgetLocal();
        if (paired != null) {
            prefs.edit().putString(PENDING_DELETE, paired.origin).apply();
            DeviceCredentialStore.Stored target = paired;
            try {
                run(() -> {
                    deleteServerRegistration(target);
                    return null;
                });
            } catch (PushException ignored) {
                // Retried on the next foreground.
            }
        }
        if (!removed) throw new PushException("keychain");
    }

    JSONObject status() {
        JSONObject result = new JSONObject();
        try {
            result.put("enabled", false).put("permission", permissionName());
            DeviceCredentialStore.Stored paired;
            try {
                paired = pairedCredential();
            } catch (PushException e) {
                paired = null;
            }
            if (!retrySecretDelete()) {
                // S may still be on disk, so a message may still decrypt:
                // never report that as off.
                result.put("enabled", true).put("error", "keychain");
                return result;
            }
            if (paired == null || !isEnabled(paired)) return result;
            result.put("enabled", true);
            try {
                ServerStatus status = serverStatus(paired);
                result.put("registered", status.registered);
                if (status.last != null) result.put("last", status.last);
            } catch (PushException ignored) {
                // Enabled locally; the server's view is unknown right now.
            }
        } catch (JSONException ignored) {
            // Only puts of known keys and values.
        }
        return result;
    }

    // Deletes the push secret, local bookkeeping and every shown notification.
    // Called on disable, unpair, revocation, credential expiry and any
    // pairing change, with or without network. It also ends the current
    // generation, so every operation started before it stops at its next
    // check and a waiting challenge is cancelled.
    // Returns false if S could not be deleted; it stays marked and is retried.
    boolean forgetLocal() {
        boolean removed = lifecycle.end(() -> {
            boolean deleted = deleteSecret();
            prefs.edit().remove(STATE_ORIGIN).remove(STATE_TOKEN).remove(STATE_ENV).apply();
            NotificationManagerCompat.from(context).cancelAll();
            return deleted;
        });
        finishPending(null, new PushException("cancelled"));
        synchronized (this) {
            lastRefresh = 0;
        }
        PushNavigator.get().reset();
        return removed;
    }

    // Taken by PushNotifier before its receiving() gate, for publish.
    int generation() {
        return lifecycle.generation();
    }

    // Posts an incoming message in the same step forgetLocal deletes and
    // cancels in, only if no forget ended `generation` and push is still on:
    // a message admitted before a disable never lands after it.
    void publish(int generation, Runnable post) {
        lifecycle.publish(generation, () -> prefs.getString(STATE_ORIGIN, null) != null, post);
    }

    // The keys for opening an envelope now, or null without a readable S or
    // while push is not on for a current, unexpired pairing (receiving). A
    // message is always judged with the current key, at display and again at
    // tap time.
    PushEnvelope.Keys currentKeys() {
        DeviceCredentialStore.Stored stored;
        try {
            stored = credentials.load();
        } catch (DeviceCredentialStore.StoreException e) {
            return null;
        }
        if (!receivingFor(stored)) return null;
        try {
            return PushEnvelope.deriveKeys(secretBox.load());
        } catch (GeneralSecurityException | IOException e) {
            return null;
        }
    }

    // Whether an incoming envelope is shown at all (PushMessages.receiving).
    // An unreadable credential with push on here still gets the generic
    // alert, without keys.
    boolean receiving() {
        if (prefs.getString(STATE_ORIGIN, null) == null) return false;
        try {
            return receivingFor(credentials.load());
        } catch (DeviceCredentialStore.StoreException e) {
            return true;
        }
    }

    private boolean receivingFor(DeviceCredentialStore.Stored stored) {
        long now = System.currentTimeMillis();
        // A credential known to be expired ends push, as in pairedCredential.
        if (stored != null && stored.expiresAtMillis <= now && hasLocalPushData()) forgetLocal();
        return PushMessages.receiving(stored == null ? null : stored.origin, stored == null ? 0 : stored.expiresAtMillis,
            prefs.getString(STATE_ORIGIN, null), now);
    }

    // MARK: lifecycle

    // On every foreground and on a new FCM token (force).
    void refreshIfNeeded(boolean force) {
        io.execute(() -> {
            retrySecretDelete();
            retryPendingDelete();
            DeviceCredentialStore.Stored paired;
            try {
                paired = pairedCredential();
            } catch (PushException e) {
                return;
            }
            if (!isEnabled(paired)) return;
            synchronized (this) {
                long now = System.currentTimeMillis();
                if (!force && lastRefresh != 0 && now - lastRefresh < REFRESH_INTERVAL_MILLIS) return;
                lastRefresh = now;
            }
            int current = lifecycle.generation();
            try {
                run(() -> {
                    sync(paired, current, false);
                    return null;
                });
            } catch (PushException e) {
                Log.i(TAG, "refresh: " + e.code);
            }
        });
    }

    // From the messaging service. See the class comment for when it answers.
    void handleChallenge(String challenge) {
        if (!PushMessages.isChallenge(challenge)) return;
        Pending current;
        synchronized (this) {
            current = pending;
        }
        if (current == null || System.currentTimeMillis() - current.startedAt >= CHALLENGE_WINDOW_MILLIS) return;
        String proof = PushEnvelope.confirmProof(challenge, current.sendKey);
        NativeHttp.Response response;
        try {
            String body = new JSONObject().put("c", challenge).put("p", proof).toString();
            response = NativeHttp.send("POST", current.relay + "/v1/confirm", relayHeaders(), body, TIMEOUT_MILLIS);
        } catch (IOException | JSONException e) {
            return;
        }
        if (response.status != 200) return;
        RelayProtocol.Handle handle = RelayProtocol.parseHandle(response.body, System.currentTimeMillis() / 1000);
        if (!RelayProtocol.confirms(handle, current.token, RelayProtocol.ENV)) return;
        finishPending(current.id, handle);
    }

    // MARK: registration

    // Every blocking step below is followed by ensureCurrent before S, state
    // or a registration is written: a generation that ended meanwhile must
    // not save a key or reach a server.
    private void sync(DeviceCredentialStore.Stored paired, int current, boolean allowNewSecret) throws PushException {
        ensureCurrent(current, paired);
        String relay = BuildConfig.MOA_PUSH_RELAY_URL;
        if (!OriginPolicy.validRelayUrl(relay)) throw new PushException("unsupported");
        ServerStatus status = serverStatus(paired);
        ensureCurrent(current, paired);
        if (!relay.equals(status.relayUrl)) throw new PushException("relay_mismatch");
        // A secret whose delete failed belongs to a disable or an old pairing.
        if (!retrySecretDelete()) throw new PushException("keychain");

        byte[] secret;
        boolean isNewSecret = false;
        synchronized (lifecycle) {
            ensureCurrent(current, paired);
            try {
                secret = secretBox.load();
                if (secret != null && secret.length != 32) throw new GeneralSecurityException("bad secret");
                if (secret == null) {
                    if (!allowNewSecret) return;
                    secret = new byte[32];
                    new SecureRandom().nextBytes(secret);
                    // Stored before anything is registered (iOS review C3).
                    secretBox.save(secret);
                    isNewSecret = true;
                }
            } catch (GeneralSecurityException | IOException e) {
                throw new PushException("keychain");
            }
            if (isNewSecret || prefs.getString(STATE_ORIGIN, null) == null) {
                prefs.edit().putString(STATE_ORIGIN, paired.origin).remove(STATE_TOKEN).remove(STATE_ENV).apply();
            }
        }

        String token = fcmToken();
        ensureCurrent(current, paired);
        String env = RelayProtocol.ENV;
        long remaining = status.expiresAt - System.currentTimeMillis() / 1000;
        // The server reports the handle-expired signal in `result`.
        String lastResult = status.last == null ? null : status.last.optString("result", null);
        if (!isNewSecret && status.registered && !"handle_expired".equals(lastResult)
            && token.equals(prefs.getString(STATE_TOKEN, null)) && env.equals(prefs.getString(STATE_ENV, null))
            && remaining > REFRESH_MARGIN_SECONDS) {
            return;
        }

        PushEnvelope.Keys keys = PushEnvelope.deriveKeys(secret);
        if (keys == null) throw new PushException("keychain");
        RelayProtocol.Handle handle = relayHandle(relay, token, keys);
        ensureCurrent(current, paired);
        postServerRegistration(paired, relay, handle, secret);
        synchronized (lifecycle) {
            ensureCurrent(current, paired);
            prefs.edit().putString(STATE_ORIGIN, paired.origin).putString(STATE_TOKEN, token).putString(STATE_ENV, env).apply();
            // The new registration replaced whatever the pending DELETE targeted.
            if (paired.origin.equals(prefs.getString(PENDING_DELETE, null))) {
                prefs.edit().remove(PENDING_DELETE).apply();
            }
        }
    }

    private void ensureCurrent(int current, DeviceCredentialStore.Stored paired) throws PushException {
        if (lifecycle.generation() != current) throw new PushException("cancelled");
        DeviceCredentialStore.Stored stored;
        try {
            stored = credentials.load();
        } catch (DeviceCredentialStore.StoreException e) {
            throw new PushException("cancelled");
        }
        if (!paired.sameAs(stored)) throw new PushException("cancelled");
    }

    private String fcmToken() throws PushException {
        if (!firebaseAvailable()) throw new PushException("unsupported");
        try {
            FirebaseMessaging messaging = FirebaseMessaging.getInstance();
            // Auto-init is off in the manifest so nothing talks to Firebase
            // before the owner turns notifications on; from then on the SDK
            // keeps the token fresh and onNewToken re-registers.
            messaging.setAutoInitEnabled(true);
            String token = Tasks.await(messaging.getToken(), TOKEN_TIMEOUT_SECONDS, TimeUnit.SECONDS);
            if (token == null || token.isEmpty()) throw new PushException("unavailable");
            return token;
        } catch (TimeoutException e) {
            throw new PushException("timeout");
        } catch (ExecutionException | InterruptedException | RuntimeException e) {
            throw new PushException("unavailable");
        }
    }

    private RelayProtocol.Handle relayHandle(String relay, String token, PushEnvelope.Keys keys) throws PushException {
        Pending registration = new Pending(relay, token, keys.send);
        synchronized (this) {
            if (pending != null) pending.result.completeExceptionally(new PushException("timeout"));
            // Armed before the request: the challenge may arrive before the
            // relay's 202 does.
            pending = registration;
        }
        io.execute(() -> {
            try {
                String body = RelayProtocol.registerBody(token, PushEnvelope.base64UrlEncode(keys.send)).toString();
                NativeHttp.Response response = NativeHttp.send("POST", relay + "/v1/register", relayHeaders(), body, TIMEOUT_MILLIS);
                if (response.status != 202) {
                    finishPending(registration.id, new PushException(response.status == 429 ? "unavailable" : "rejected"));
                }
            } catch (IOException | JSONException e) {
                finishPending(registration.id, new PushException("unavailable"));
            }
        });
        try {
            return registration.result.get(CHALLENGE_WINDOW_MILLIS, TimeUnit.MILLISECONDS);
        } catch (TimeoutException e) {
            finishPending(registration.id, new PushException("timeout"));
            throw new PushException("timeout");
        } catch (ExecutionException e) {
            throw e.getCause() instanceof PushException ? (PushException) e.getCause() : new PushException("unavailable");
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new PushException("cancelled");
        }
    }

    // Completes the pending registration if it is still `id` (or any, for null).
    private void finishPending(String id, Object outcome) {
        Pending current;
        synchronized (this) {
            current = pending;
            if (current == null || (id != null && !current.id.equals(id))) return;
            pending = null;
        }
        if (outcome instanceof RelayProtocol.Handle) {
            current.result.complete((RelayProtocol.Handle) outcome);
        } else {
            current.result.completeExceptionally((Throwable) outcome);
        }
    }

    // MARK: paired server

    private ServerStatus serverStatus(DeviceCredentialStore.Stored paired) throws PushException {
        NativeHttp.Response response = server("GET", paired, "/api/push/native/status", null);
        switch (response.status) {
            case 200:
                break;
            case 401:
            case 403:
                forgetIfCurrent(paired);
                throw new PushException("not_paired");
            case 503:
                throw new PushException("unavailable");
            default:
                throw new PushException("rejected");
        }
        JSONObject object;
        try {
            object = new JSONObject(response.body);
        } catch (JSONException e) {
            throw new PushException("rejected");
        }
        JSONObject last = null;
        JSONObject raw = object.optJSONObject("last");
        if (raw != null) {
            last = new JSONObject();
            for (String key : new String[] { "at", "result", "reason" }) {
                Object value = raw.opt(key);
                if (value instanceof String || value instanceof Number) {
                    try {
                        last.put(key, value);
                    } catch (JSONException ignored) {
                        // Known key, plain value.
                    }
                }
            }
        }
        String relayUrl = object.opt("relay_url") instanceof String ? object.optString("relay_url") : null;
        return new ServerStatus(object.optBoolean("registered", false), relayUrl, object.optLong("expires_at", 0), last);
    }

    private void postServerRegistration(DeviceCredentialStore.Stored paired, String relay, RelayProtocol.Handle handle,
        byte[] secret) throws PushException {
        JSONObject body;
        try {
            // The iOS five fields plus the provider (absent means APNs).
            body = new JSONObject()
                .put("relay_url", relay)
                .put("handle", handle.handle)
                .put("expires_at", handle.expiresAt)
                .put("secret", PushEnvelope.base64UrlEncode(secret))
                .put("env", RelayProtocol.ENV)
                .put("provider", RelayProtocol.PROVIDER);
        } catch (JSONException e) {
            throw new PushException("rejected");
        }
        NativeHttp.Response response = server("POST", paired, "/api/push/native", body.toString());
        if (response.status >= 200 && response.status < 300) return;
        switch (response.status) {
            case 400:
                String code = null;
                try {
                    code = new JSONObject(response.body).optString("error", null);
                } catch (JSONException ignored) {
                    // Not JSON: a plain rejection.
                }
                throw new PushException("relay_mismatch".equals(code) ? "relay_mismatch" : "rejected");
            case 401:
            case 403:
                forgetIfCurrent(paired);
                throw new PushException("not_paired");
            case 503:
                throw new PushException("unavailable");
            default:
                throw new PushException("rejected");
        }
    }

    // The server said this credential is revoked or expired (iOS review I4).
    // Only if it is still the stored one: a late answer for a previous
    // pairing must not wipe the current one.
    private void forgetIfCurrent(DeviceCredentialStore.Stored paired) {
        try {
            if (paired.sameAs(credentials.load())) forgetLocal();
        } catch (DeviceCredentialStore.StoreException ignored) {
            // Unreadable: nothing to compare against.
        }
    }

    private void deleteServerRegistration(DeviceCredentialStore.Stored paired) {
        try {
            NativeHttp.Response response = server("DELETE", paired, "/api/push/native", null);
            if ((response.status >= 200 && response.status < 300) || response.status == 404) {
                prefs.edit().remove(PENDING_DELETE).apply();
            }
        } catch (PushException ignored) {
            // Kept for the next foreground.
        }
    }

    // The mark is cleared only by a DELETE that reached the server or by a
    // later successful POST (sync), never by enabling again locally.
    private void retryPendingDelete() {
        String origin = prefs.getString(PENDING_DELETE, null);
        if (origin == null) return;
        DeviceCredentialStore.Stored paired;
        try {
            paired = credentials.load();
        } catch (DeviceCredentialStore.StoreException e) {
            return;
        }
        if (paired == null || !paired.origin.equals(origin)) {
            // Unpaired or paired elsewhere: this credential has nothing to delete.
            prefs.edit().remove(PENDING_DELETE).apply();
            return;
        }
        try {
            run(() -> {
                // A POST queued ahead of this one may have replaced it already.
                if (origin.equals(prefs.getString(PENDING_DELETE, null))) deleteServerRegistration(paired);
                return null;
            });
        } catch (PushException ignored) {
            // Kept for the next foreground.
        }
    }

    // Native header auth only; NativeHttp never carries the WebView cookie.
    private NativeHttp.Response server(String method, DeviceCredentialStore.Stored paired, String path, String body)
        throws PushException {
        String origin = OriginPolicy.normalizeOrigin(paired.origin);
        if (origin == null) throw new PushException("not_paired");
        Map<String, String> headers = new HashMap<>();
        headers.put("Authorization", "Moa-Device " + paired.credential);
        if (!"GET".equals(method)) headers.put("X-Moa-Request", "1");
        if (body != null) headers.put("Content-Type", "application/json");
        try {
            return NativeHttp.send(method, origin + path, headers, body, TIMEOUT_MILLIS);
        } catch (IOException e) {
            throw new PushException("unavailable");
        }
    }

    // MARK: local state

    private interface Operation {
        Void run() throws PushException;
    }

    // Runs on the serial executor and waits for it: FIFO with every other
    // server/relay operation, whatever its outcome.
    private void run(Operation operation) throws PushException {
        Future<Void> future = operations.submit(operation::run);
        try {
            future.get();
        } catch (ExecutionException e) {
            throw e.getCause() instanceof PushException ? (PushException) e.getCause() : new PushException("unavailable");
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new PushException("cancelled");
        }
    }

    private DeviceCredentialStore.Stored pairedCredential() throws PushException {
        DeviceCredentialStore.Stored stored;
        try {
            stored = credentials.load();
        } catch (DeviceCredentialStore.StoreException e) {
            throw new PushException("keychain");
        }
        if (stored == null || OriginPolicy.normalizeOrigin(stored.origin) == null) throw new PushException("not_paired");
        if (stored.expiresAtMillis <= System.currentTimeMillis()) {
            // A credential known to be expired ends push here too (iOS review I4).
            if (hasLocalPushData()) forgetLocal();
            throw new PushException("not_paired");
        }
        // A different pairing never inherits the previous S (iOS review C3).
        String stateOrigin = prefs.getString(STATE_ORIGIN, null);
        if (stateOrigin != null && !stateOrigin.equals(stored.origin)) forgetLocal();
        return stored;
    }

    private boolean isEnabled(DeviceCredentialStore.Stored paired) {
        if (!paired.origin.equals(prefs.getString(STATE_ORIGIN, null))) return false;
        try {
            return secretBox.load() != null;
        } catch (GeneralSecurityException | IOException e) {
            return true;
        }
    }

    // A read error counts as data: forgetting is the safe side.
    private boolean hasLocalPushData() {
        if (prefs.getString(STATE_ORIGIN, null) != null) return true;
        try {
            return secretBox.load() != null;
        } catch (GeneralSecurityException | IOException e) {
            return true;
        }
    }

    private boolean deleteSecret() {
        try {
            secretBox.delete();
        } catch (GeneralSecurityException | IOException e) {
            prefs.edit().putBoolean(SECRET_DELETE_PENDING, true).commit();
            return false;
        }
        prefs.edit().remove(SECRET_DELETE_PENDING).commit();
        return true;
    }

    // False while a secret that should be gone may still be readable. The
    // mark is read and acted on under the lifecycle, like the S writes: a
    // retry that saw an old mark cannot delete an S enabled since.
    private boolean retrySecretDelete() {
        synchronized (lifecycle) {
            if (!prefs.getBoolean(SECRET_DELETE_PENDING, false)) return true;
            return deleteSecret();
        }
    }

    // Android 13+ asks at runtime; below that the app-level switch decides.
    private boolean requestPermission() throws PushException {
        NotificationManagerCompat manager = NotificationManagerCompat.from(context);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU
            && ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            PermissionRequester requester = permissionRequester;
            if (requester == null) throw new PushException("unavailable");
            prefs.edit().putBoolean(PERMISSION_ASKED, true).apply();
            try {
                if (!Boolean.TRUE.equals(requester.request().get(120, TimeUnit.SECONDS))) return false;
            } catch (ExecutionException | TimeoutException e) {
                return false;
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                throw new PushException("cancelled");
            }
        }
        return manager.areNotificationsEnabled();
    }

    private String permissionName() {
        if (NotificationManagerCompat.from(context).areNotificationsEnabled()) return "granted";
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && !prefs.getBoolean(PERMISSION_ASKED, false)) {
            return "not_determined";
        }
        return "denied";
    }

    // Without google-services.json at build time there is no Firebase app:
    // push reports "unsupported" instead of crashing.
    private boolean firebaseAvailable() {
        try {
            return !FirebaseApp.getApps(context).isEmpty();
        } catch (RuntimeException e) {
            return false;
        }
    }

    private static Map<String, String> relayHeaders() {
        Map<String, String> headers = new HashMap<>();
        headers.put("Content-Type", "application/json");
        return headers;
    }
}
