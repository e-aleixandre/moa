package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
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

// Moa-Voice and the k:"call" envelope for the incoming-call work, pinned to
// relay/test/voice-vectors.json, which the Go server generates (VoiceAuthKey,
// SealEnvelope). Read, not copied.
public class VoiceAuthTest {

    private static JSONObject vectors;
    private static byte[] secret;

    @BeforeClass
    public static void load() throws Exception {
        String root = System.getProperty("moa.repoRoot");
        vectors = new JSONObject(new String(
            Files.readAllBytes(Paths.get(root == null ? "../../.." : root, "relay", "test", "voice-vectors.json")),
            StandardCharsets.UTF_8));
        secret = PushEnvelope.base64UrlDecode(vectors.getJSONObject("device").getString("S"));
    }

    private static String hex(byte[] bytes) {
        StringBuilder out = new StringBuilder();
        for (byte b : bytes) out.append(String.format("%02x", b));
        return out.toString();
    }

    @Test
    public void headerEqualsWhatTheServerDerives() throws Exception {
        JSONObject device = vectors.getJSONObject("device");
        assertEquals(device.getString("voice_key"), hex(PushEnvelope.derive(secret, "moa-voice-auth-v1", 32)));
        assertEquals(device.getString("header"), VoiceAuth.header(device.getString("device_id"), secret));
    }

    @Test
    public void refusesASecretThatIsNot32Bytes() {
        assertNull(VoiceAuth.header("dev_1", new byte[31]));
        assertNull(VoiceAuth.header("dev_1", new byte[33]));
        assertNull(VoiceAuth.header("dev_1", null));
    }

    @Test
    public void refusesAnUnusableDeviceId() {
        assertNull(VoiceAuth.header(null, secret));
        assertNull(VoiceAuth.header("", secret));
        assertNull(VoiceAuth.header("dev.1", secret));
        assertNull(VoiceAuth.header("dev 1", secret));
    }

    @Test
    public void deviceIdIsWhatPrecedesTheFirstDotOfTheCredential() {
        assertEquals("dev_0123", VoiceAuth.deviceId("dev_0123.secretpart.more"));
        assertNull(VoiceAuth.deviceId("nodot"));
        assertNull(VoiceAuth.deviceId(".secret"));
        assertNull(VoiceAuth.deviceId(""));
        assertNull(VoiceAuth.deviceId(null));
    }

    private Map<String, String> callEnvelope() throws Exception {
        JSONObject e = vectors.getJSONObject("call").getJSONObject("envelope");
        Map<String, String> data = new HashMap<>();
        data.put("v", String.valueOf(e.getInt("v")));
        data.put("k", e.getString("k"));
        data.put("c", e.getString("c"));
        return data;
    }

    @Test
    public void opensTheCallEnvelopeTheServerSealed() throws Exception {
        JSONObject call = vectors.getJSONObject("call");
        PushEnvelope.Keys keys = PushEnvelope.deriveKeys(secret);
        PushEnvelope.Content content = PushEnvelope.open(callEnvelope(), keys, call.getLong("now"));
        assertNotNull(content);
        assertEquals("call", content.kind);
        assertEquals(call.getString("inv"), content.invite);
        assertEquals(call.getLong("exp"), content.expiresAt);
        assertEquals(call.getString("name"), content.title);
        assertEquals(PushEnvelope.Destination.session(call.getString("session")), content.destination);
    }

    @Test
    public void aCallEnvelopePastItsExpiryDoesNotOpen() throws Exception {
        JSONObject call = vectors.getJSONObject("call");
        PushEnvelope.Keys keys = PushEnvelope.deriveKeys(secret);
        assertNotNull(PushEnvelope.open(callEnvelope(), keys, call.getLong("exp") - 1));
        assertNull(PushEnvelope.open(callEnvelope(), keys, call.getLong("exp")));
    }

    @Test
    public void inviteFromTheCallEnvelope() throws Exception {
        JSONObject call = vectors.getJSONObject("call");
        PushEnvelope.Content content = PushEnvelope.open(callEnvelope(), PushEnvelope.deriveKeys(secret), call.getLong("now"));
        CallInvite invite = CallInvite.from(content);
        assertNotNull(invite);
        assertEquals(call.getString("inv"), invite.id);
        assertEquals("Moa", invite.name);
        assertEquals("sess_1", invite.sessionId);
        assertEquals(call.getLong("exp"), invite.expiresAtSeconds);
    }

    @Test
    public void onlyACallToASessionIsAnInvite() {
        String uuid = "0f8fad5b-d9cb-469f-a165-70867728950e";
        PushEnvelope.Destination session = PushEnvelope.Destination.session("sess_1");
        assertNotNull(CallInvite.from(content("call", uuid, session)));
        assertNull("not a call", CallInvite.from(content("ask", uuid, session)));
        assertNull("no invite", CallInvite.from(content("call", null, session)));
        assertNull("not a uuid", CallInvite.from(content("call", "not-a-uuid", session)));
        assertNull("short uuid", CallInvite.from(content("call", "1-1-1-1-1", session)));
        assertNull("not a session", CallInvite.from(content("call", uuid, PushEnvelope.Destination.HOME)));
        assertNull(CallInvite.from(null));
    }

    @Test
    public void inviteNameIsCutLikeTheCallScreenOne() {
        String uuid = "0f8fad5b-d9cb-469f-a165-70867728950e";
        StringBuilder title = new StringBuilder();
        for (int i = 0; i < 200; i++) title.append('x');
        CallInvite invite = CallInvite.from(content("call", uuid, PushEnvelope.Destination.session("s"), title.toString()));
        assertEquals(80, invite.name.length());
        assertTrue(invite.name.startsWith("xx"));
    }

    private static PushEnvelope.Content content(String kind, String invite, PushEnvelope.Destination destination) {
        return content(kind, invite, destination, "Moa");
    }

    private static PushEnvelope.Content content(String kind, String invite, PushEnvelope.Destination destination, String title) {
        return new PushEnvelope.Content("id", destination, PushEnvelope.Level.URGENT, "th", title, "b", kind, invite, 1790000045L);
    }
}
