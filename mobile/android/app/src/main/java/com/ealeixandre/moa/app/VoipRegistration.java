package com.ealeixandre.moa.app;

import java.io.IOException;
import java.security.SecureRandom;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import org.json.JSONException;
import org.json.JSONObject;

// Keeps this phone's incoming-call destination registered: the Android port of
// iOS NativeIncomingCalls.refreshRegistration and handleChallenge. It is the
// second destination on the same FCM token (the first is the alerts'), with
// its own secret S_v, registered at the relay as type "voip" and then at the
// paired moa server. Only with the app on screen: the challenge must not ring
// a locked phone, and a token is only worth registering for a server that can
// use it.
//
// refresh() blocks (network, the challenge wait), so the host runs it off the
// main thread. A generation counter, moved on by forgetLocal, fences a
// registration that was started before an unpair: it stops before writing
// S_v, state or a destination, and removes the one it just made.
final class VoipRegistration {

    interface Host {
        boolean foreground();

        // The paired credential, or null when unpaired or unreadable.
        DeviceCredentialStore.Stored paired();

        // The relay this build talks to, or null when there is none.
        String relayUrl();

        // The FCM token (the alerts' one); may block.
        String fcmToken() throws IOException;

        long now();

        // The relay and the paired server over HTTPS, no redirects, no cookies.
        NativeHttp.Response send(String method, String url, Map<String, String> headers, String body) throws IOException;

        // Runs `task` once after the delay, off the main thread.
        void retryLater(long delayMillis, Runnable task);
    }

    // The last registered token, origin and env (not secret).
    interface Memory {
        String get(String key);

        void put(String key, String value);

        void clear();
    }

    private static final class Pending {
        final String token;
        final byte[] sendKey;
        final String relay;
        final long startedAt;
        final CompletableFuture<RelayProtocol.Handle> result = new CompletableFuture<>();

        Pending(String token, byte[] sendKey, String relay, long startedAt) {
            this.token = token;
            this.sendKey = sendKey;
            this.relay = relay;
            this.startedAt = startedAt;
        }
    }

    private static final long REFRESH_INTERVAL_MILLIS = 3_600_000;
    private static final long REFRESH_MARGIN_SECONDS = 30L * 24 * 3600;
    static final long CHALLENGE_WINDOW_MILLIS = 300_000;
    // After a failed registration (the relay refused the token, offline, the
    // server refused): when to try again without waiting for the app to be
    // reopened. Three tries, then the next foreground is the next chance.
    private static final long[] RETRY_DELAYS_MILLIS = {15_000, 60_000, 300_000};

    private final Host host;
    private final CallSecretStore secrets;
    private final Memory memory;
    private final long challengeWindowMillis;
    private volatile int generation;
    private Pending pending;
    private boolean registering;
    private long lastRefresh;
    private int retries;
    private boolean retryScheduled;

    VoipRegistration(Host host, CallSecretStore secrets, Memory memory) {
        this(host, secrets, memory, CHALLENGE_WINDOW_MILLIS);
    }

    VoipRegistration(Host host, CallSecretStore secrets, Memory memory, long challengeWindowMillis) {
        this.host = host;
        this.secrets = secrets;
        this.memory = memory;
        this.challengeWindowMillis = challengeWindowMillis;
    }

    // On every foreground, and when FCM issues a new token (force).
    void refresh(boolean force) {
        DeviceCredentialStore.Stored paired = host.paired();
        if (!host.foreground()) {
            // A token FCM rotated while the app was away is due at the next
            // foreground, not an hour later.
            if (force) {
                synchronized (this) {
                    lastRefresh = 0;
                }
            }
            return;
        }
        if (paired == null || paired.expiresAtMillis <= host.now() || !paired.origin.startsWith("https://")) return;
        String deviceId = VoiceAuth.deviceId(paired.credential);
        if (deviceId == null) return;
        int current;
        synchronized (this) {
            long now = host.now();
            if (registering) return;
            if (!force && lastRefresh != 0 && now - lastRefresh < REFRESH_INTERVAL_MILLIS) return;
            lastRefresh = now;
            registering = true;
            current = generation;
        }
        boolean failed = false;
        try {
            failed = !run(paired, deviceId, current);
        } catch (IOException e) {
            // Offline.
            failed = true;
        } finally {
            synchronized (this) {
                if (generation == current) registering = false;
            }
        }
        if (failed) failed(current);
    }

    // A registration that did not happen must not count as the hour's
    // attempt (the next foreground tries again) and is retried by itself with
    // a growing delay, so a token FCM had not accepted yet does not leave the
    // phone unreachable until the app is reopened.
    private void failed(int current) {
        long delay;
        synchronized (this) {
            if (generation != current) return;
            lastRefresh = 0;
            if (retryScheduled || retries >= RETRY_DELAYS_MILLIS.length) return;
            retryScheduled = true;
            delay = RETRY_DELAYS_MILLIS[retries++];
        }
        host.retryLater(delay, () -> retry(current));
    }

    // A retry belongs to the pairing that scheduled it: after a forget it is
    // dropped, and the new pairing keeps its own budget.
    private void retry(int scheduledBy) {
        synchronized (this) {
            if (generation != scheduledBy) return;
            retryScheduled = false;
        }
        refresh(true);
    }

    // False when the registration was attempted and did not happen; true when
    // it did, was not needed, or was stopped on purpose.
    private boolean run(DeviceCredentialStore.Stored paired, String deviceId, int current) throws IOException {
        NativeHttp.Response status = server("GET", paired, "/api/push/voip/status", null);
        // An older server without incoming calls answers 404; nothing to do.
        if (status.status != 200 || generation != current) return true;
        JSONObject report;
        try {
            report = new JSONObject(status.body);
        } catch (JSONException e) {
            return true;
        }
        String token = host.fcmToken();
        if (token == null || token.isEmpty() || generation != current) return true;
        String relay = host.relayUrl();
        if (relay == null || !relay.equals(report.optString("relay_url", null))) return true;

        CallSecretStore.Secret stored = secrets.load();
        if (stored != null && (!stored.origin.equals(paired.origin) || !stored.deviceId.equals(deviceId))) stored = null;
        String env = RelayProtocol.ENV;
        long remaining = report.optLong("expires_at", 0) - host.now() / 1000;
        JSONObject last = report.optJSONObject("last");
        boolean expired = last != null && "handle_expired".equals(last.optString("result", null));
        if (stored != null && report.optBoolean("registered", false) && !expired
            && paired.origin.equals(memory.get("origin")) && token.equals(memory.get("token"))
            && env.equals(memory.get("env")) && remaining > REFRESH_MARGIN_SECONDS) {
            return true;
        }

        CallSecretStore.Secret secret = stored;
        if (secret == null) {
            byte[] bytes = new byte[32];
            new SecureRandom().nextBytes(bytes);
            secret = new CallSecretStore.Secret(paired.origin, deviceId, bytes);
            // Kept before anything is registered with it.
            synchronized (this) {
                if (generation != current || !secrets.save(secret)) return true;
            }
        }
        PushEnvelope.Keys keys = PushEnvelope.deriveKeys(secret.secret);
        if (keys == null) return true;
        RelayProtocol.Handle handle = relayHandle(relay, token, keys, current);
        if (generation != current) return true;
        if (handle == null) return false;

        JSONObject body;
        try {
            body = new JSONObject()
                .put("relay_url", relay)
                .put("handle", handle.handle)
                .put("expires_at", handle.expiresAt)
                .put("secret", PushEnvelope.base64UrlEncode(secret.secret))
                .put("env", env)
                .put("provider", RelayProtocol.PROVIDER);
        } catch (JSONException e) {
            return true;
        }
        NativeHttp.Response posted = server("POST", paired, "/api/push/voip", body.toString());
        boolean ok = posted.status >= 200 && posted.status < 300;
        synchronized (this) {
            if (generation == current) {
                if (ok) {
                    memory.put("origin", paired.origin);
                    memory.put("token", token);
                    memory.put("env", env);
                    retries = 0;
                }
                return ok;
            }
        }
        // Forgotten while this was on its way: what it created goes.
        if (ok) unregister(paired);
        return true;
    }

    // Registers at the relay and waits for its challenge, which arrives as an
    // FCM message (handleChallenge), until the window closes.
    private RelayProtocol.Handle relayHandle(String relay, String token, PushEnvelope.Keys keys, int current) throws IOException {
        Pending registration = new Pending(token, keys.send, relay, host.now());
        synchronized (this) {
            // The same exclusion forgetLocal uses: a forget either cancels
            // what is armed here or is seen here, never lands in between.
            if (generation != current) return null;
            if (pending != null) pending.result.complete(null);
            // Armed before the request: the challenge may beat the 202.
            pending = registration;
        }
        try {
            NativeHttp.Response response = relayPost(relay + "/v1/register",
                RelayProtocol.voipRegisterBody(token, PushEnvelope.base64UrlEncode(keys.send)).toString());
            if (response.status != 202) return null;
            return registration.result.get(challengeWindowMillis, TimeUnit.MILLISECONDS);
        } catch (JSONException | TimeoutException | ExecutionException e) {
            return null;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return null;
        } finally {
            synchronized (this) {
                if (pending == registration) pending = null;
            }
        }
    }

    // From the messaging service, for any challenge. Answers only one for the
    // registration started in the last five minutes, and takes the handle
    // only for that token, env and the voip type: alerts share the FCM
    // token, and an alert challenge reaches here too (its proof would fail).
    void handleChallenge(String challenge) {
        Pending current;
        synchronized (this) {
            current = pending;
        }
        if (current == null || host.now() - current.startedAt >= CHALLENGE_WINDOW_MILLIS || !PushMessages.isChallenge(challenge)) return;
        try {
            String body = new JSONObject().put("c", challenge).put("p", PushEnvelope.confirmProof(challenge, current.sendKey)).toString();
            NativeHttp.Response response = relayPost(current.relay + "/v1/confirm", body);
            if (response.status != 200) return;
            RelayProtocol.Handle handle = RelayProtocol.parseHandle(response.body, host.now() / 1000);
            if (!RelayProtocol.confirms(handle, current.token, RelayProtocol.ENV, "voip")) return;
            synchronized (this) {
                if (pending == current) current.result.complete(handle);
            }
        } catch (IOException | JSONException e) {
            // Unanswered: the registration times out.
        }
    }

    // Unpairing, a new pairing, a revoked credential: the secret goes, so this
    // phone can neither open invitations nor answer any more. False while S_v
    // may still be readable.
    boolean forgetLocal() {
        Pending waiting;
        boolean deleted;
        synchronized (this) {
            generation++;
            registering = false;
            lastRefresh = 0;
            retries = 0;
            retryScheduled = false;
            waiting = pending;
            pending = null;
            deleted = secrets.delete();
            memory.clear();
        }
        if (waiting != null) waiting.result.complete(null);
        return deleted;
    }

    // Best effort: the server drops the destination. A server that never
    // hears of it keeps a destination this phone can no longer open, which
    // simply never rings.
    void unregister(DeviceCredentialStore.Stored paired) {
        try {
            server("DELETE", paired, "/api/push/voip", null);
        } catch (IOException ignored) {
            // Nothing more to try.
        }
    }

    private NativeHttp.Response server(String method, DeviceCredentialStore.Stored paired, String path, String body) throws IOException {
        String origin = OriginPolicy.normalizeOrigin(paired.origin);
        if (origin == null) throw new IOException("not paired");
        Map<String, String> headers = new HashMap<>();
        headers.put("Authorization", "Moa-Device " + paired.credential);
        if (!"GET".equals(method)) headers.put("X-Moa-Request", "1");
        if (body != null) headers.put("Content-Type", "application/json");
        return host.send(method, origin + path, headers, body);
    }

    private NativeHttp.Response relayPost(String url, String body) throws IOException {
        Map<String, String> headers = new HashMap<>();
        headers.put("Content-Type", "application/json");
        return host.send("POST", url, headers, body);
    }
}
