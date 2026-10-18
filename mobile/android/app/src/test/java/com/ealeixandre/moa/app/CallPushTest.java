package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Paths;
import java.util.HashMap;
import java.util.Map;
import org.json.JSONObject;
import org.junit.BeforeClass;
import org.junit.Test;

// One FCM token carries two destinations, alerts and calls, each with its own
// secret: the envelope's key id says which one a message is for, and a
// registration challenge is only answered by the type that is waiting on it.
public class CallPushTest {

    private static JSONObject vectors;
    private static PushEnvelope.Keys callKeys;
    private static PushEnvelope.Keys alertKeys;

    @BeforeClass
    public static void load() throws Exception {
        String root = System.getProperty("moa.repoRoot");
        vectors = new JSONObject(new String(
            Files.readAllBytes(Paths.get(root == null ? "../../.." : root, "relay", "test", "voice-vectors.json")),
            StandardCharsets.UTF_8));
        callKeys = PushEnvelope.deriveKeys(PushEnvelope.base64UrlDecode(vectors.getJSONObject("device").getString("S")));
        byte[] other = new byte[32];
        other[0] = 1;
        alertKeys = PushEnvelope.deriveKeys(other);
    }

    private static Map<String, String> callEnvelope() throws Exception {
        JSONObject e = vectors.getJSONObject("call").getJSONObject("envelope");
        Map<String, String> data = new HashMap<>();
        data.put("v", String.valueOf(e.getInt("v")));
        data.put("k", e.getString("k"));
        data.put("c", e.getString("c"));
        return data;
    }

    // ---- which secret an envelope is for

    @Test
    public void theKeyIdPicksTheCallSecretNotTheAlertOne() throws Exception {
        Map<String, String> data = callEnvelope();
        assertTrue(PushMessages.addressedTo(data, callKeys));
        assertFalse(PushMessages.addressedTo(data, alertKeys));
    }

    @Test
    public void noKeysMeansNoEnvelopeIsForThem() throws Exception {
        assertFalse(PushMessages.addressedTo(callEnvelope(), null));
        assertFalse(PushMessages.addressedTo(null, callKeys));
        Map<String, String> noKid = callEnvelope();
        noKid.remove("k");
        assertFalse(PushMessages.addressedTo(noKid, callKeys));
        Map<String, String> bad = callEnvelope();
        bad.put("k", "not a kid!");
        assertFalse(PushMessages.addressedTo(bad, callKeys));
    }

    @Test
    public void aCallEnvelopeBecomesAnInviteWithItsInvitationAndExpiry() throws Exception {
        JSONObject call = vectors.getJSONObject("call");
        CallInvite invite = CallInvite.fromPush(callEnvelope(), callKeys, call.getLong("now"));
        assertNotNull(invite);
        assertEquals(call.getString("inv"), invite.id);
        assertEquals(call.getLong("exp"), invite.expiresAtSeconds);
    }

    @Test
    public void anExpiredWrongKeyOrNotACallIsNoInvite() throws Exception {
        JSONObject call = vectors.getJSONObject("call");
        assertNull("expired", CallInvite.fromPush(callEnvelope(), callKeys, call.getLong("exp")));
        assertNull("other secret", CallInvite.fromPush(callEnvelope(), alertKeys, call.getLong("now")));
        assertNull("no keys", CallInvite.fromPush(callEnvelope(), null, call.getLong("now")));
        Map<String, String> tampered = callEnvelope();
        String c = tampered.get("c");
        char flipped = c.charAt(40) == 'A' ? 'B' : 'A';
        tampered.put("c", c.substring(0, 40) + flipped + c.substring(41));
        assertNull("tampered", CallInvite.fromPush(tampered, callKeys, call.getLong("now")));
    }

    // ---- which type a challenge's confirmation is for

    private static final long NOW = 1_790_000_000L;
    private static final String HANDLE = "h".repeat(60);

    private static RelayProtocol.Handle handle(String type) {
        String body = "{\"handle\":\"" + HANDLE + "\",\"expires_at\":" + (NOW + 3600) + ",\"token\":\"aa\",\"env\":\"production\",\"provider\":\"fcm\""
            + (type == null ? "" : ",\"type\":\"" + type + "\"") + "}";
        return RelayProtocol.parseHandle(body, NOW);
    }

    @Test
    public void aCallRegistrationOnlyAcceptsAVoipConfirmation() {
        assertTrue(RelayProtocol.confirms(handle("voip"), "aa", "production", "voip"));
        assertFalse(RelayProtocol.confirms(handle("alert"), "aa", "production", "voip"));
        assertFalse("an older relay says no type", RelayProtocol.confirms(handle(null), "aa", "production", "voip"));
        assertFalse(RelayProtocol.confirms(handle("voip"), "bb", "production", "voip"));
    }

    @Test
    public void anAlertRegistrationRefusesAVoipConfirmationButAcceptsAnOlderRelay() {
        assertFalse(RelayProtocol.confirms(handle("voip"), "aa", "production"));
        assertTrue(RelayProtocol.confirms(handle("alert"), "aa", "production"));
        assertTrue(RelayProtocol.confirms(handle(null), "aa", "production"));
    }

    @Test
    public void theCallRegisterBodyAsksForAVoipFcmDestination() throws Exception {
        JSONObject body = RelayProtocol.voipRegisterBody("tok", "key");
        assertEquals("tok", body.getString("token"));
        assertEquals("production", body.getString("env"));
        assertEquals("key", body.getString("send_key"));
        assertEquals("fcm", body.getString("provider"));
        assertEquals("voip", body.getString("type"));
        assertEquals(5, body.length());
        // The alert registration stays as it was.
        assertFalse(RelayProtocol.registerBody("tok", "key").has("type"));
    }
}
