package com.ealeixandre.moa.app;

// Moa-Voice: the narrow credential an incoming-call destination derives from
// its own secret S_v, so a locked phone can follow, answer and decline an
// invitation and join its call socket, and nothing else. The server derives
// the same key (push.VoiceAuthKey); relay/test/voice-vectors.json pins both.
// iOS: IncomingCallAuth.
final class VoiceAuth {

    private VoiceAuth() {}

    // "Moa-Voice <deviceID>.<b64u(HKDF-SHA256(S_v, no salt, "moa-voice-auth-v1", 32))>",
    // or null for a secret that is not 32 bytes or an unusable device id.
    static String header(String deviceId, byte[] secret) {
        if (secret == null || secret.length != 32) return null;
        if (deviceId == null || deviceId.isEmpty() || deviceId.indexOf('.') >= 0) return null;
        for (int i = 0; i < deviceId.length(); i++) {
            if (!PushEnvelope.isUrlSafe(deviceId.charAt(i))) return null;
        }
        return "Moa-Voice " + deviceId + "." + PushEnvelope.base64UrlEncode(PushEnvelope.derive(secret, "moa-voice-auth-v1", 32));
    }

    // The device id is what precedes the first dot of the Moa-Device credential.
    static String deviceId(String credential) {
        if (credential == null) return null;
        int dot = credential.indexOf('.');
        return dot > 0 ? credential.substring(0, dot) : null;
    }
}
