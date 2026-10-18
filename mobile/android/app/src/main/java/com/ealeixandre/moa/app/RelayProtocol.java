package com.ealeixandre.moa.app;

import org.json.JSONException;
import org.json.JSONObject;

// The relay's answers the app relies on (relay/PROTOCOL.md §1), as pure
// functions for the JVM tests.
public final class RelayProtocol {

    public static final String PROVIDER = "fcm";
    // FCM has no sandbox: the relay's env for an FCM token is always this.
    public static final String ENV = "production";
    static final long MAXIMUM_HANDLE_LIFETIME = 91L * 24 * 3600;

    public static final class Handle {
        public final String handle;
        public final long expiresAt;
        public final String token;
        public final String env;
        public final String provider;
        // "alert" | "voip"; null from a relay that predates types.
        public final String type;

        Handle(String handle, long expiresAt, String token, String env, String provider, String type) {
            this.handle = handle;
            this.expiresAt = expiresAt;
            this.token = token;
            this.env = env;
            this.provider = provider;
            this.type = type;
        }
    }

    private RelayProtocol() {}

    public static JSONObject registerBody(String token, String sendKey) throws JSONException {
        return new JSONObject()
            .put("token", token)
            .put("env", ENV)
            .put("send_key", sendKey)
            .put("provider", PROVIDER);
    }

    // The incoming-call destination: the same FCM token, a separate handle.
    public static JSONObject voipRegisterBody(String token, String sendKey) throws JSONException {
        return registerBody(token, sendKey).put("type", "voip");
    }

    public static Handle parseHandle(String body, long nowSeconds) {
        JSONObject object;
        try {
            object = new JSONObject(body);
        } catch (JSONException | NullPointerException e) {
            return null;
        }
        Object handle = object.opt("handle");
        Object expires = object.opt("expires_at");
        if (!(handle instanceof String) || !(expires instanceof Integer || expires instanceof Long)) return null;
        String text = (String) handle;
        if (text.length() < 40 || text.length() > 1024) return null;
        for (int i = 0; i < text.length(); i++) {
            if (!PushEnvelope.isUrlSafe(text.charAt(i))) return null;
        }
        long expiresAt = ((Number) expires).longValue();
        if (expiresAt <= nowSeconds || expiresAt > nowSeconds + MAXIMUM_HANDLE_LIFETIME) return null;
        return new Handle(text, expiresAt, string(object, "token"), string(object, "env"), string(object, "provider"), string(object, "type"));
    }

    // Binds a confirmation to the registration the app is waiting on (iOS
    // RelayConfirmation, review I2): the relay returns the destination the
    // challenge was sealed for, and only the pending one is accepted. A
    // relay that does not say which destination (or provider) it confirmed
    // is refused, never trusted: S, hence K_send, is reused on renewal, so a
    // late challenge for an earlier token would otherwise verify too.
    public static boolean confirms(Handle handle, String token, String env) {
        // An older relay says no type: that is an alert destination.
        return confirms(handle, token, env, "alert");
    }

    // type: the destination type the registration asked for. Alerts and calls
    // share one FCM token and one challenge channel, so a confirmation for
    // the other type is never taken for this one.
    public static boolean confirms(Handle handle, String token, String env, String type) {
        String confirmed = handle == null ? null : handle.type;
        if (confirmed == null && "alert".equals(type)) confirmed = "alert";
        return handle != null
            && type.equals(confirmed)
            && handle.token != null
            && handle.env != null
            && handle.provider != null
            && handle.token.equals(token)
            && handle.env.equals(env)
            && handle.provider.equals(PROVIDER);
    }

    private static String string(JSONObject object, String name) {
        Object value = object.opt(name);
        return value instanceof String ? (String) value : null;
    }
}
