package com.ealeixandre.moa.app;

import org.json.JSONException;
import org.json.JSONObject;

// The wire constants and small pure rules of a native call. Everything here
// is the iOS contract (NativeVoiceCall.swift), not new protocol.
final class CallProtocol {

    static final int SAMPLE_RATE = 24000;
    // 20 ms of s16le mono.
    static final int FRAME_BYTES = 480 * 2;
    // One second of captured audio in flight (the page's MAX_SOCKET_BUFFERED).
    static final int MAX_SENDING_BYTES = 48000;
    // Sixty seconds queued to play (the page's PLAYBACK_MAX_SECONDS).
    static final int MAX_QUEUED_BYTES = SAMPLE_RATE * 2 * 60;
    // Audio that arrives before the speaker is ready is kept this long.
    static final int PENDING_MAX_BYTES = SAMPLE_RATE * 2 * 4;
    static final long HANGUP_TIMEOUT_MILLIS = 5000;
    // The WebSocket close reason limit.
    static final int MAX_REASON_BYTES = 123;
    static final String HANGUP_FRAME = "{\"type\":\"hangup\"}";

    enum Control {
        NONE,
        // The server's barge-in: stop what is playing.
        INTERRUPT,
        ENDED
    }

    private CallProtocol() {}

    static String muteFrame(boolean muted) {
        return "{\"type\":\"mute\",\"muted\":" + muted + "}";
    }

    // The part of a server text frame the app acts on itself; the page gets
    // every frame anyway.
    static Control parseControl(String text) {
        if (text == null) return Control.NONE;
        JSONObject message;
        try {
            message = new JSONObject(text);
        } catch (JSONException e) {
            return Control.NONE;
        }
        Object type = message.opt("type");
        if (!(type instanceof String)) return Control.NONE;
        switch ((String) type) {
            case "speech_started":
                return Control.INTERRUPT;
            case "state":
                return Boolean.TRUE.equals(message.opt("interrupt")) ? Control.INTERRUPT : Control.NONE;
            case "ended":
                return Control.ENDED;
            default:
                return Control.NONE;
        }
    }

    // Fresh frames are dropped, never queued, while a second is in flight.
    static boolean dropFrame(long queuedBytes, int frameBytes) {
        return queuedBytes + frameBytes > MAX_SENDING_BYTES;
    }

    // "native:<why> mic=<state>": the only way to learn, from the server's
    // journal, why the app ended a call on a phone with no cable.
    static String closeReason(String why, String micState) {
        return closeReason("native:" + (why == null || why.isEmpty() ? "unspecified" : why) + " mic=" + micState);
    }

    // At most 123 bytes of UTF-8, never cut inside a character.
    static String closeReason(String text) {
        StringBuilder out = new StringBuilder();
        int bytes = 0;
        for (int i = 0; i < text.length(); ) {
            int cp = text.codePointAt(i);
            int n = cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
            if (bytes + n > MAX_REASON_BYTES) break;
            out.appendCodePoint(cp);
            bytes += n;
            i += Character.charCount(cp);
        }
        return out.toString();
    }

    // The first n characters (code points), as Swift's String.prefix.
    static String prefix(String text, int n) {
        if (text == null) return "";
        if (text.codePointCount(0, text.length()) <= n) return text;
        return text.substring(0, text.offsetByCodePoints(0, n));
    }
}
