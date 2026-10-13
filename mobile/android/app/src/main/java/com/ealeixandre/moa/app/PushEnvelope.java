package com.ealeixandre.moa.app;

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.util.Arrays;
import java.util.Base64;
import java.util.Map;
import javax.crypto.Cipher;
import javax.crypto.Mac;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;
import org.json.JSONException;
import org.json.JSONObject;

// Wire contract v1 of moa native push (relay/PROTOCOL.md), the Android port of
// ios/App/PushEnvelope.swift. Pure Java: the vector tests run it on the JVM
// against relay/test/vectors.json, the same file the Go server, the relay
// and the Swift code reproduce.
public final class PushEnvelope {

    public static final int PADDED_SIZE = 2048;
    public static final int NONCE_SIZE = 12;
    public static final int TAG_SIZE = 16;
    public static final int KID_SIZE = 8;
    public static final int ENCODED_CIPHERTEXT_LENGTH = 2768;
    public static final int ENCODED_KID_LENGTH = 11;
    static final byte[] AAD_LABEL = "moa-push-v1".getBytes(StandardCharsets.US_ASCII);

    public enum Level {
        URGENT,
        ACTIVE,
        PASSIVE
    }

    public static final class Keys {
        public final byte[] enc;
        public final byte[] send;
        public final byte[] kid;

        public Keys(byte[] enc, byte[] send, byte[] kid) {
            this.enc = enc;
            this.send = send;
            this.kid = kid;
        }
    }

    // Only the paired origin plus one of three fixed query shapes: nothing
    // from the envelope can choose a host, path or arbitrary parameter.
    public static final class Destination {
        public static final Destination HOME = new Destination("home", null);
        public static final Destination INBOX = new Destination("inbox", null);

        public final String kind;
        public final String session;

        private Destination(String kind, String session) {
            this.kind = kind;
            this.session = session;
        }

        public static Destination session(String id) {
            return isSessionId(id) ? new Destination("session", id) : HOME;
        }

        public String url(String pairedOrigin) {
            String origin = OriginPolicy.normalizeOrigin(pairedOrigin);
            if (origin == null) return null;
            switch (kind) {
                case "session":
                    return origin + "/?session=" + session;
                case "inbox":
                    return origin + "/?inbox=1";
                default:
                    return origin + "/";
            }
        }

        @Override
        public boolean equals(Object other) {
            if (!(other instanceof Destination)) return false;
            Destination that = (Destination) other;
            return kind.equals(that.kind) && (session == null ? that.session == null : session.equals(that.session));
        }

        @Override
        public int hashCode() {
            return kind.hashCode() * 31 + (session == null ? 0 : session.hashCode());
        }

        @Override
        public String toString() {
            return session == null ? kind : kind + ":" + session;
        }
    }

    public static final class Content {
        public final String id;
        public final Destination destination;
        public final Level level;
        public final String thread;
        public final String title;
        public final String body;

        Content(String id, Destination destination, Level level, String thread, String title, String body) {
            this.id = id;
            this.destination = destination;
            this.level = level;
            this.thread = thread;
            this.title = title;
            this.body = body;
        }
    }

    private PushEnvelope() {}

    // HKDF-SHA256 (RFC 5869) with an empty salt; the label is the info.
    public static byte[] derive(byte[] secret, String label, int length) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(new byte[32], "HmacSHA256"));
            byte[] prk = mac.doFinal(secret);
            mac.init(new SecretKeySpec(prk, "HmacSHA256"));
            byte[] info = label.getBytes(StandardCharsets.UTF_8);
            byte[] out = new byte[length];
            byte[] block = new byte[0];
            int written = 0;
            for (int counter = 1; written < length; counter++) {
                mac.update(block);
                mac.update(info);
                mac.update((byte) counter);
                block = mac.doFinal();
                int n = Math.min(block.length, length - written);
                System.arraycopy(block, 0, out, written, n);
                written += n;
            }
            return out;
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException(e);
        }
    }

    public static Keys deriveKeys(byte[] secret) {
        if (secret == null || secret.length != 32) return null;
        return new Keys(
            derive(secret, "moa-push-enc-v1", 32),
            derive(secret, "moa-push-send-v1", 32),
            derive(secret, "moa-push-kid-v1", KID_SIZE)
        );
    }

    // Authenticates before interpreting anything. Any failure -- shape, kid,
    // tag, inner version, expiry -- returns null and the caller falls back.
    // `envelope` is the FCM data map: every value is a string there, so the
    // outer version is the string "1".
    public static Content open(Map<String, String> envelope, Keys keys, long nowSeconds) {
        if (envelope == null || keys == null) return null;
        if (!"1".equals(envelope.get("v"))) return null;
        String kidText = envelope.get("k");
        String sealedText = envelope.get("c");
        if (kidText == null || kidText.length() != ENCODED_KID_LENGTH) return null;
        if (sealedText == null || sealedText.length() != ENCODED_CIPHERTEXT_LENGTH) return null;
        byte[] kid = base64UrlDecode(kidText);
        if (kid == null || !MessageDigest.isEqual(kid, keys.kid)) return null;
        byte[] sealed = base64UrlDecode(sealedText);
        if (sealed == null || sealed.length != NONCE_SIZE + PADDED_SIZE + TAG_SIZE) return null;

        byte[] padded;
        try {
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(
                Cipher.DECRYPT_MODE,
                new SecretKeySpec(keys.enc, "AES"),
                new GCMParameterSpec(TAG_SIZE * 8, sealed, 0, NONCE_SIZE)
            );
            cipher.updateAAD(concat(AAD_LABEL, keys.kid));
            padded = cipher.doFinal(sealed, NONCE_SIZE, sealed.length - NONCE_SIZE);
        } catch (GeneralSecurityException e) {
            return null;
        }
        if (padded.length != PADDED_SIZE) return null;

        int end = padded.length;
        while (end > 0 && padded[end - 1] == 0x20) end--;
        JSONObject plaintext;
        try {
            plaintext = new JSONObject(new String(padded, 0, end, StandardCharsets.UTF_8));
        } catch (JSONException e) {
            return null;
        }
        Object version = plaintext.opt("v");
        Object exp = plaintext.opt("exp");
        if (!(version instanceof Integer) || (Integer) version != 1) return null;
        if (!(exp instanceof Integer || exp instanceof Long)) return null;
        if (((Number) exp).longValue() <= nowSeconds) return null;
        String d = stringField(plaintext, "d");
        String lvl = stringField(plaintext, "lvl");
        String th = stringField(plaintext, "th");
        String t = stringField(plaintext, "t");
        String b = stringField(plaintext, "b");
        if (d == null || lvl == null || th == null || t == null || b == null) return null;

        Destination destination;
        switch (d) {
            case "session":
                destination = Destination.session(stringField(plaintext, "s"));
                break;
            case "inbox":
                destination = Destination.INBOX;
                break;
            default:
                destination = Destination.HOME;
        }
        return new Content(stringField(plaintext, "id"), destination, level(lvl), th, t, b);
    }

    // An unknown level from a newer server still sounds rather than being
    // silently demoted (same as iOS).
    static Level level(String raw) {
        switch (raw) {
            case "urgent":
                return Level.URGENT;
            case "passive":
                return Level.PASSIVE;
            default:
                return Level.ACTIVE;
        }
    }

    // p = b64u(HMAC(K_send, "moa-confirm-v1" || 0x00 || ascii(c)))
    public static String confirmProof(String challenge, byte[] sendKey) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(sendKey, "HmacSHA256"));
            mac.update("moa-confirm-v1".getBytes(StandardCharsets.US_ASCII));
            mac.update((byte) 0);
            mac.update(challenge.getBytes(StandardCharsets.US_ASCII));
            return base64UrlEncode(mac.doFinal());
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException(e);
        }
    }

    public static boolean isSessionId(String value) {
        if (value == null || value.isEmpty() || value.length() > 128) return false;
        for (int i = 0; i < value.length(); i++) {
            if (!isUrlSafe(value.charAt(i))) return false;
        }
        return true;
    }

    static boolean isUrlSafe(char c) {
        return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '-' || c == '_';
    }

    public static String base64UrlEncode(byte[] data) {
        return Base64.getUrlEncoder().withoutPadding().encodeToString(data);
    }

    // Unpadded base64url only; standard-alphabet or padded input is refused.
    public static byte[] base64UrlDecode(String text) {
        if (text == null) return null;
        for (int i = 0; i < text.length(); i++) {
            if (!isUrlSafe(text.charAt(i))) return null;
        }
        if (text.length() % 4 == 1) return null;
        try {
            return Base64.getUrlDecoder().decode(text);
        } catch (IllegalArgumentException e) {
            return null;
        }
    }

    private static String stringField(JSONObject object, String name) {
        Object value = object.opt(name);
        return value instanceof String ? (String) value : null;
    }

    static byte[] concat(byte[] a, byte[] b) {
        byte[] out = Arrays.copyOf(a, a.length + b.length);
        System.arraycopy(b, 0, out, a.length, b.length);
        return out;
    }
}
