package com.ealeixandre.moa.app;

import org.json.JSONException;
import org.json.JSONObject;

// One message of window.MoaNativeCall to the app: {op, id, ...}. The same
// shape the iOS script posts to its message handler.
final class NativeCallMessage {

    final String op;
    final String id;
    final String url;
    final String name;
    final String sessionId;
    final String text;
    final boolean muted;

    private NativeCallMessage(JSONObject body, String op, String id) {
        this.op = op;
        this.id = id;
        this.url = string(body, "url", "");
        this.name = string(body, "name", "moa");
        this.sessionId = string(body, "sessionId", "");
        this.text = string(body, "text", null);
        this.muted = Boolean.TRUE.equals(body.opt("muted"));
    }

    // What the system call and its notification are named.
    String displayName() {
        return name;
    }

    // Only a real string counts, as iOS's `as? String`.
    private static String string(JSONObject body, String key, String fallback) {
        Object value = body.opt(key);
        return value instanceof String ? (String) value : fallback;
    }

    // null unless it is an object with a string op and a string id ("" for
    // active).
    static NativeCallMessage parse(String json) {
        if (json == null) return null;
        JSONObject body;
        try {
            body = new JSONObject(json);
        } catch (JSONException e) {
            return null;
        }
        Object op = body.opt("op");
        Object id = body.opt("id");
        if (!(op instanceof String) || !(id instanceof String)) return null;
        return new NativeCallMessage(body, (String) op, (String) id);
    }

    // The same switch as the iOS handler's ops.
    void applyTo(NativeCallManager manager) {
        switch (op) {
            case "open":
                manager.open(id, url, name, sessionId);
                break;
            case "active":
                manager.reportActive();
                break;
            case "send":
                if (text != null) manager.send(id, text);
                break;
            case "close":
                manager.closeFromPage(id);
                break;
            case "startAudio":
                manager.startAudio(id);
                break;
            case "stopAudio":
                manager.stopAudio(id);
                break;
            case "setMuted":
                manager.setMutedFromPage(id, muted);
                break;
            default:
        }
    }
}
