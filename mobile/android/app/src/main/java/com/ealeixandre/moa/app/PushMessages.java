package com.ealeixandre.moa.app;

import java.util.Map;
import org.json.JSONObject;

// Pure decisions about one FCM data message: what kind it is, which channel
// it goes to, which notification it replaces, and whether it is shown at
// all. Kept apart from the Android notification code so the JVM tests pin it.
public final class PushMessages {

    public enum Kind {
        // {"r":c}: a registration challenge. Never shown.
        CHALLENGE,
        // {v,k,c,col?}: an encrypted envelope. Shown, decrypted or not, while
        // push is on here (receiving).
        ENVELOPE,
        // Anything else did not come from the relay's closed schema.
        IGNORE
    }

    public static final String CHANNEL_URGENT = "moa_urgent";
    public static final String CHANNEL_ACTIVE = "moa_active";
    public static final String CHANNEL_PASSIVE = "moa_passive";

    // NotificationManager.IMPORTANCE_* values, copied so this class stays pure.
    static final int IMPORTANCE_LOW = 2;
    static final int IMPORTANCE_DEFAULT = 3;
    static final int IMPORTANCE_HIGH = 4;

    private PushMessages() {}

    public static Kind classify(Map<String, String> data) {
        if (data == null) return Kind.IGNORE;
        if (data.containsKey("r") && !data.containsKey("c")) return Kind.CHALLENGE;
        if (data.containsKey("c")) return Kind.ENVELOPE;
        return Kind.IGNORE;
    }

    // urgent: "Preguntas y permisos" (heads-up, sound); active: "Terminadas"
    // (sound); passive: "Resúmenes" (silent). An envelope that cannot be
    // opened is shown on the active channel, which sounds: losing a question
    // is worse than an extra generic alert (decision 30/09), and the urgent
    // channel would make every undecryptable message a heads-up.
    public static String channelFor(PushEnvelope.Level level) {
        if (level == null) return CHANNEL_ACTIVE;
        switch (level) {
            case URGENT:
                return CHANNEL_URGENT;
            case PASSIVE:
                return CHANNEL_PASSIVE;
            default:
                return CHANNEL_ACTIVE;
        }
    }

    public static String fallbackChannel() {
        return CHANNEL_ACTIVE;
    }

    public static int importanceFor(String channel) {
        switch (channel) {
            case CHANNEL_URGENT:
                return IMPORTANCE_HIGH;
            case CHANNEL_PASSIVE:
                return IMPORTANCE_LOW;
            default:
                return IMPORTANCE_DEFAULT;
        }
    }

    // The notification tag decides replacement: the relay's opaque collapse
    // id when present (two digests of one project leave one notification),
    // otherwise the envelope's own id so distinct alerts stack. The collapse
    // id comes from the relay in the clear, so it is only used if it has the
    // shape the server produces.
    public static String tagFor(Map<String, String> data, PushEnvelope.Content content) {
        String collapse = data == null ? null : data.get("col");
        if (collapse != null && collapse.length() <= 64 && PushEnvelope.isSessionId(collapse)) {
            return "col:" + collapse;
        }
        if (content != null && content.id != null && PushEnvelope.isSessionId(content.id)) {
            return "id:" + content.id;
        }
        String sealed = data == null ? null : data.get("c");
        // The ciphertext starts with the random nonce: unique per message.
        if (sealed != null && sealed.length() >= 16) return "c:" + sealed.substring(0, 16);
        return "moa";
    }

    // The tap intent's data: the whole tag. PendingIntent identity ignores
    // extras, so without it two notifications whose tags share a hashCode
    // would share one PendingIntent and a tap could open the other one's
    // session. Tags are URL-safe characters and ':' (tagFor), valid in an
    // opaque URI as they are.
    static String tapUri(String tag) {
        return "moa-push:" + tag;
    }

    // Whether an envelope may be shown at all, even as the generic alert, and
    // its keys used: push is on here (stateOrigin, cleared by disable, unpair
    // and any pairing change) for the stored pairing, whose credential is not
    // known to be expired. A revocation the server has not told us about yet
    // cannot be known offline and is not judged here.
    static boolean receiving(String pairedOrigin, long expiresAtMillis, String stateOrigin, long nowMillis) {
        return pairedOrigin != null && pairedOrigin.equals(stateOrigin) && expiresAtMillis > nowMillis;
    }

    // Same rule as iOS willPresent: no banner for the session already on
    // screen, and only while the app is actually in the foreground.
    public static boolean suppress(PushEnvelope.Content content, String visibleSession, boolean foreground) {
        if (!foreground || content == null || visibleSession == null) return false;
        return "session".equals(content.destination.kind) && visibleSession.equals(content.destination.session);
    }

    public static boolean isChallenge(String challenge) {
        return challenge != null
            && !challenge.isEmpty()
            && challenge.length() <= 2048
            && PushEnvelope.base64UrlDecode(challenge) != null;
    }

    // Calls window.MoaNavigation in the page. The destination is a JSON
    // literal built from a validated id, never spliced as code. The script
    // never navigates by itself: a false answer goes back to Java, at once
    // through the evaluateJavascript result, or, when the page answers with
    // a Promise, later as an openFallback message on the push port carrying
    // `token`. Java then asks NavigationGate before loading the URL.
    static String openInPlaceScript(PushEnvelope.Destination destination, int token) {
        String call = "session".equals(destination.kind)
            ? "nav.openSession(" + JSONObject.quote(destination.session) + ")"
            : "nav.openInbox()";
        return "(() => {"
            + " const nav = window.MoaNavigation;"
            + " if (!(nav && nav.version >= 1)) return false;"
            + " const port = window." + WebBridges.PUSH_PORT + ";"
            + " const fallback = () => { if (port) port.postMessage(JSON.stringify("
            + "{ id: \"openFallback\", method: \"openFallback\", options: { token: " + token + " } })); };"
            + " try {"
            + "  const result = " + call + ";"
            + "  if (result && typeof result.then === \"function\") {"
            + "   result.then((ok) => { if (!ok) fallback(); }, fallback);"
            + "   return true;"
            + "  }"
            + "  return !!result;"
            + " } catch (e) { return false; }"
            + "})()";
    }
}
