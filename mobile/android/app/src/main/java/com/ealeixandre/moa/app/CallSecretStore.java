package com.ealeixandre.moa.app;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import org.json.JSONException;
import org.json.JSONObject;

// S_v, the secret of this phone's incoming-call destination (iOS
// IncomingCallSecretStore): drawn on the device, given to the server only with
// the destination, never the alert secret. It also derives the Moa-Voice key
// that answers calls with the screen locked, so it is bound to the origin and
// device it was made for. Legible after the first unlock, as the credential.
final class CallSecretStore {

    static final class Secret {
        final String origin;
        final String deviceId;
        final byte[] secret;

        Secret(String origin, String deviceId, byte[] secret) {
            this.origin = origin;
            this.deviceId = deviceId;
            this.secret = secret;
        }

        // The Moa-Voice header, or null for a secret that cannot make one.
        String authorization() {
            return VoiceAuth.header(deviceId, secret);
        }
    }

    private final SecretBox box;

    CallSecretStore(SecretBox box) {
        this.box = box;
    }

    // Null when absent, unreadable or malformed: a secret that cannot be used
    // is as good as none, and registration makes a new one.
    Secret load() {
        byte[] data;
        try {
            data = box.load();
        } catch (GeneralSecurityException | IOException e) {
            return null;
        }
        if (data == null) return null;
        try {
            JSONObject object = new JSONObject(new String(data, StandardCharsets.UTF_8));
            String origin = object.optString("origin", "");
            String deviceId = object.optString("device_id", "");
            byte[] secret = PushEnvelope.base64UrlDecode(object.optString("secret", ""));
            if (origin.isEmpty() || deviceId.isEmpty() || secret == null || secret.length != 32) return null;
            return new Secret(origin, deviceId, secret);
        } catch (JSONException e) {
            return null;
        }
    }

    boolean save(Secret secret) {
        if (secret.secret == null || secret.secret.length != 32) return false;
        try {
            JSONObject object = new JSONObject()
                .put("origin", secret.origin)
                .put("device_id", secret.deviceId)
                .put("secret", PushEnvelope.base64UrlEncode(secret.secret));
            box.save(object.toString().getBytes(StandardCharsets.UTF_8));
            return true;
        } catch (JSONException | GeneralSecurityException | IOException e) {
            return false;
        }
    }

    // False while a secret that should be gone may still be readable.
    boolean delete() {
        try {
            box.delete();
            return true;
        } catch (GeneralSecurityException | IOException e) {
            return false;
        }
    }
}
