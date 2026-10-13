package com.ealeixandre.moa.app;

import android.content.Context;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import org.json.JSONException;
import org.json.JSONObject;

// The paired server origin, the durable device credential (device_id.secret)
// and its expiry, as one Keystore-encrypted value in no-backup storage. Never
// in SharedPreferences, a URL, the WebView, JavaScript or a log. Same shape as
// iOS DeviceCredentialStore. The origin is not secret, but keeping it in the
// same sealed value means the paired origin the bridges trust is exactly the
// one the credential belongs to.
final class DeviceCredentialStore {

    static final class Stored {
        final String origin;
        final String credential;
        final long expiresAtMillis;

        Stored(String origin, String credential, long expiresAtMillis) {
            this.origin = origin;
            this.credential = credential;
            this.expiresAtMillis = expiresAtMillis;
        }

        boolean sameAs(Stored other) {
            return other != null && origin.equals(other.origin) && credential.equals(other.credential);
        }
    }

    static final class StoreException extends Exception {
        StoreException(Throwable cause) {
            super(cause);
        }
    }

    private static DeviceCredentialStore shared;

    private final KeystoreBox box;
    // Navigation and every bridge call ask for the paired origin; decrypting
    // each time would put a Keystore operation on the UI thread per request.
    private volatile String cachedOrigin;
    private volatile boolean originLoaded;

    static synchronized DeviceCredentialStore get(Context context) {
        if (shared == null) shared = new DeviceCredentialStore(context.getApplicationContext());
        return shared;
    }

    private DeviceCredentialStore(Context context) {
        box = new KeystoreBox(context, "moa-device-credential-v1", "device-credential.bin");
    }

    synchronized Stored load() throws StoreException {
        byte[] data;
        try {
            data = box.load();
        } catch (GeneralSecurityException | IOException e) {
            throw new StoreException(e);
        }
        if (data == null) {
            remember(null);
            return null;
        }
        try {
            JSONObject object = new JSONObject(new String(data, StandardCharsets.UTF_8));
            Stored stored = new Stored(object.getString("origin"), object.getString("credential"), object.getLong("expires_at_ms"));
            remember(stored.origin);
            return stored;
        } catch (JSONException e) {
            throw new StoreException(e);
        }
    }

    synchronized void save(Stored stored) throws StoreException {
        try {
            JSONObject object = new JSONObject()
                .put("origin", stored.origin)
                .put("credential", stored.credential)
                .put("expires_at_ms", stored.expiresAtMillis);
            box.save(object.toString().getBytes(StandardCharsets.UTF_8));
        } catch (JSONException | GeneralSecurityException | IOException e) {
            throw new StoreException(e);
        }
        remember(stored.origin);
    }

    synchronized void delete() throws StoreException {
        // Whatever happens below, nothing may treat the old origin as paired.
        remember(null);
        try {
            box.delete();
        } catch (GeneralSecurityException | IOException e) {
            throw new StoreException(e);
        }
    }

    // The origin the bridges and navigation trust, or null when unpaired or
    // unreadable (fail closed).
    String pairedOrigin() {
        if (originLoaded) return cachedOrigin;
        try {
            load();
        } catch (StoreException e) {
            return null;
        }
        return cachedOrigin;
    }

    private void remember(String origin) {
        cachedOrigin = origin;
        originLoaded = true;
    }
}
