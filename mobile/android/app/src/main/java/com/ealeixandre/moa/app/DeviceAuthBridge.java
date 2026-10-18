package com.ealeixandre.moa.app;

import android.content.Context;
import android.os.Handler;
import android.os.Looper;
import android.webkit.CookieManager;
import java.io.IOException;
import java.time.OffsetDateTime;
import java.time.format.DateTimeParseException;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import org.json.JSONException;
import org.json.JSONObject;

// window.MoaNativeAuth: the Android port of iOS DeviceAuthBridge.swift.
// The one-time pairing claim and the durable device credential stay in native
// code; the page gets only success or a classified error code. The credential
// is exchanged natively (Authorization: Moa-Device) for the short-lived
// __Host-moa_device cookie, which goes straight into the WebView's
// CookieManager: HttpOnly, so page JavaScript never reads either secret.
final class DeviceAuthBridge implements WebBridges.MethodHandler {

    static final class AuthException extends Exception {
        final String code;

        AuthException(String code) {
            super(code);
            this.code = code;
        }
    }

    private static final int TIMEOUT_MILLIS = 15_000;

    private final Context context;
    private final DeviceCredentialStore store;
    // One at a time, in order: claim then authorize from the same page must
    // not interleave with a reset.
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());

    DeviceAuthBridge(Context context, DeviceCredentialStore store) {
        this.context = context.getApplicationContext();
        this.store = store;
    }

    @Override
    public void handle(String method, JSONObject options, OriginPolicy.Caller caller, WebBridges.Reply reply) {
        worker.execute(() -> {
            try {
                switch (method) {
                    case "claim":
                        claim(validatedOrigin(options.optString("origin", null)),
                            options.optString("payload", null), options.optString("deviceLabel", null));
                        reply.ok(null);
                        return;
                    case "authorize":
                        authorize(validatedOrigin(options.optString("origin", null)));
                        reply.ok(null);
                        return;
                    case "reauthorize":
                        DeviceCredentialStore.Stored stored = loadStored();
                        if (stored == null) throw new AuthException("not_paired");
                        authorize(validatedOrigin(stored.origin));
                        reply.ok(null);
                        return;
                    case "reset":
                        reset();
                        reply.ok(null);
                        return;
                    default:
                        reply.fail("unsupported");
                }
            } catch (AuthException e) {
                reply.fail(e.code);
            } catch (RuntimeException e) {
                reply.fail("unavailable");
            }
        });
    }

    private void claim(String origin, String payload, String deviceLabel) throws AuthException {
        String label = deviceLabel == null ? "" : deviceLabel.trim();
        if (payload == null || label.isEmpty() || label.codePointCount(0, label.length()) > 80) {
            throw new AuthException("invalid");
        }
        String[] parts = payload.split(":", -1);
        if (parts.length != 3 || !"moa-pair-v1".equals(parts[0]) || parts[1].isEmpty() || parts[2].isEmpty()) {
            throw new AuthException("invalid");
        }

        NativeHttp.Response response;
        try {
            String body = new JSONObject()
                .put("pairing_id", parts[1])
                .put("pairing_secret", parts[2])
                .put("device_label", label)
                .toString();
            response = NativeHttp.send("POST", origin + "/api/pulse/pairings/claim", jsonHeaders(null), body, TIMEOUT_MILLIS);
        } catch (IOException | JSONException e) {
            throw new AuthException("unavailable");
        }
        if (response.status != 201) {
            int s = response.status;
            throw new AuthException(s == 400 || s == 401 || s == 403 || s == 429 ? "claim_rejected" : "unavailable");
        }

        String deviceId;
        String credential;
        long expiresAt;
        try {
            JSONObject result = new JSONObject(response.body);
            deviceId = result.getString("device_id");
            credential = result.getString("credential");
            expiresAt = OffsetDateTime.parse(result.getString("expires_at")).toInstant().toEpochMilli();
        } catch (JSONException | DateTimeParseException e) {
            throw new AuthException("invalid");
        }
        if (deviceId.isEmpty() || !credential.startsWith(deviceId + ".") || expiresAt <= System.currentTimeMillis()) {
            throw new AuthException("invalid");
        }
        try {
            store.save(new DeviceCredentialStore.Stored(origin, credential, expiresAt));
        } catch (DeviceCredentialStore.StoreException e) {
            throw new AuthException("keychain");
        }
        // A new pairing never inherits the previous push secret (iOS review
        // C3). After the save, so push work for the old credential is fenced.
        NativePushRegistration.get(context).forgetLocal();
        AndroidVoip.forget(context, null);
        NativeCalls.bindingChanged();
    }

    private void authorize(String origin) throws AuthException {
        DeviceCredentialStore.Stored stored = loadStored();
        if (stored == null || !stored.origin.equals(origin) || stored.expiresAtMillis <= System.currentTimeMillis()) {
            throw new AuthException("not_paired");
        }
        NativeHttp.Response response;
        try {
            response = NativeHttp.send("POST", origin + "/api/pulse/device-session",
                jsonHeaders(stored.credential), "{}", TIMEOUT_MILLIS);
        } catch (IOException e) {
            throw new AuthException("unavailable");
        }
        if (response.status == 401 || response.status == 403) {
            NativePushRegistration.get(context).forgetLocal();
            AndroidVoip.forget(context, null);
            try {
                store.delete();
            } catch (DeviceCredentialStore.StoreException ignored) {
                // The origin is already forgotten in memory; the next launch retries.
            }
            setCookie(origin, SessionCookie.expired());
            NativeCalls.bindingChanged();
            throw new AuthException("not_paired");
        }
        if (response.status != 204) throw new AuthException("unavailable");
        String cookie = SessionCookie.select(response.header("Set-Cookie"));
        if (cookie == null) throw new AuthException("invalid");
        if (!setCookie(origin, cookie)) throw new AuthException("unavailable");
    }

    private void reset() throws AuthException {
        // Push keys and notifications go first and regardless of the
        // credential delete below: unpairing must stop decryption offline.
        boolean pushSecretRemoved = NativePushRegistration.get(context).forgetLocal();
        String origin = null;
        DeviceCredentialStore.Stored paired = null;
        try {
            paired = store.load();
            if (paired != null) origin = paired.origin;
        } catch (DeviceCredentialStore.StoreException ignored) {
            // Deleting below is still the right thing.
        }
        // The call destination too, and the server is told while the
        // credential can still say it.
        AndroidVoip.forget(context, paired);
        AuthException deleteError = null;
        try {
            store.delete();
        } catch (DeviceCredentialStore.StoreException e) {
            deleteError = new AuthException("keychain");
        }
        if (origin != null) setCookie(origin, SessionCookie.expired());
        NativeCalls.bindingChanged();
        if (deleteError != null) throw deleteError;
        if (!pushSecretRemoved) throw new AuthException("keychain");
    }

    private DeviceCredentialStore.Stored loadStored() throws AuthException {
        try {
            return store.load();
        } catch (DeviceCredentialStore.StoreException e) {
            throw new AuthException("keychain");
        }
    }

    // CookieManager is a UI-thread API here; wait for its callback so the
    // page only navigates once the session cookie is really in place.
    private boolean setCookie(String origin, String header) {
        CountDownLatch done = new CountDownLatch(1);
        AtomicBoolean ok = new AtomicBoolean(false);
        main.post(() -> {
            CookieManager manager = CookieManager.getInstance();
            manager.setCookie(origin, header, (Boolean accepted) -> {
                ok.set(Boolean.TRUE.equals(accepted));
                manager.flush();
                done.countDown();
            });
        });
        try {
            if (!done.await(5, TimeUnit.SECONDS)) return false;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        }
        return ok.get();
    }

    private static Map<String, String> jsonHeaders(String credential) {
        Map<String, String> headers = new HashMap<>();
        headers.put("Content-Type", "application/json");
        headers.put("X-Moa-Request", "1");
        if (credential != null) headers.put("Authorization", "Moa-Device " + credential);
        return headers;
    }

    private static String validatedOrigin(String raw) throws AuthException {
        String origin = OriginPolicy.normalizeOrigin(raw);
        if (origin == null) throw new AuthException("invalid");
        return origin;
    }
}
