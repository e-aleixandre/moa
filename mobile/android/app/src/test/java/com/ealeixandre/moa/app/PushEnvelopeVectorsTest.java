package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.security.SecureRandom;
import java.util.Arrays;
import java.util.HashMap;
import java.util.Map;
import javax.crypto.Cipher;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;
import org.json.JSONObject;
import org.junit.BeforeClass;
import org.junit.Test;

// Pins the Android side of moa native push to relay/test/vectors.json, which
// the Go server, the relay and the Swift code also reproduce. The file is read,
// not copied: a regenerated vector set is checked here without editing tests.
public class PushEnvelopeVectorsTest {

    private static JSONObject vectors;
    private static PushEnvelope.Keys keys;
    private static long now;
    private static long expiry;

    @BeforeClass
    public static void load() throws Exception {
        String root = System.getProperty("moa.repoRoot");
        Path path = Paths.get(root == null ? "../../.." : root, "relay", "test", "vectors.json");
        vectors = new JSONObject(new String(Files.readAllBytes(path), StandardCharsets.UTF_8));
        keys = PushEnvelope.deriveKeys(PushEnvelope.base64UrlDecode(device().getString("S")));
        now = vectors.getJSONObject("envelope").getLong("now");
        expiry = new JSONObject(vectors.getJSONObject("envelope").getString("plaintext")).getLong("exp");
    }

    private static JSONObject device() throws Exception {
        return vectors.getJSONObject("device");
    }

    private static Map<String, String> envelope() throws Exception {
        JSONObject e = vectors.getJSONObject("envelope").getJSONObject("envelope");
        Map<String, String> data = new HashMap<>();
        data.put("v", String.valueOf(e.getInt("v")));
        data.put("k", e.getString("k"));
        data.put("c", e.getString("c"));
        return data;
    }

    @Test
    public void derivesDeviceKeys() throws Exception {
        byte[] s = PushEnvelope.base64UrlDecode(device().getString("S"));
        assertEquals(32, s.length);
        assertEquals(device().getString("K_enc"), hex(keys.enc));
        assertEquals(device().getString("K_send"), hex(keys.send));
        assertEquals(device().getString("kid"), hex(keys.kid));
        assertEquals(device().getString("K_col"), hex(PushEnvelope.derive(s, "moa-push-collapse-v1", 32)));
        assertEquals(envelope().get("k"), PushEnvelope.base64UrlEncode(keys.kid));
    }

    @Test
    public void rejectsSecretOfWrongLength() {
        assertNull(PushEnvelope.deriveKeys(new byte[31]));
        assertNull(PushEnvelope.deriveKeys(null));
    }

    @Test
    public void opensVectorEnvelope() throws Exception {
        JSONObject notification = vectors.getJSONObject("envelope").getJSONObject("notification");
        assertEquals(PushEnvelope.ENCODED_CIPHERTEXT_LENGTH, envelope().get("c").length());
        PushEnvelope.Content content = PushEnvelope.open(envelope(), keys, now);
        assertNotNull(content);
        assertEquals(notification.getString("title"), content.title);
        assertEquals(notification.getString("body"), content.body);
        assertEquals(vectors.getJSONObject("collapse").getString(notification.getString("tag")), content.thread);
        assertEquals(PushEnvelope.Destination.session(notification.getString("session_id")), content.destination);
        assertEquals(PushEnvelope.Level.URGENT, content.level);
        assertEquals(vectors.getJSONObject("envelope").getString("id"), content.id);
        assertEquals("https://moa.example:8443/?session=sess_1", content.destination.url("https://moa.example:8443"));
    }

    @Test
    public void confirmProofMatchesVector() throws Exception {
        JSONObject confirm = vectors.getJSONObject("confirm");
        assertEquals(confirm.getString("p"), PushEnvelope.confirmProof(confirm.getString("c"), keys.send));
    }

    @Test
    public void rejectsTamperedCiphertext() throws Exception {
        String c = envelope().get("c");
        assertNull("body", PushEnvelope.open(with("c", flip(c, 100)), keys, now));
        assertNull("nonce", PushEnvelope.open(with("c", flip(c, 5)), keys, now));
        assertNull("tag", PushEnvelope.open(with("c", flip(c, c.length() - 3)), keys, now));
    }

    @Test
    public void rejectsWrongKid() throws Exception {
        assertNull(PushEnvelope.open(with("k", PushEnvelope.base64UrlEncode(new byte[8])), keys, now));
    }

    @Test
    public void rejectsAnotherDevicesKey() throws Exception {
        byte[] seven = new byte[32];
        Arrays.fill(seven, (byte) 7);
        PushEnvelope.Keys other = PushEnvelope.deriveKeys(seven);
        assertNull(PushEnvelope.open(envelope(), other, now));
        // Even with the right kid the tag fails under another K_enc.
        assertNull(PushEnvelope.open(envelope(), new PushEnvelope.Keys(other.enc, other.send, keys.kid), now));
    }

    @Test
    public void rejectsExpired() throws Exception {
        assertNotNull(PushEnvelope.open(envelope(), keys, expiry - 1));
        assertNull(PushEnvelope.open(envelope(), keys, expiry));
    }

    @Test
    public void rejectsMalformedEnvelopes() throws Exception {
        String c = envelope().get("c");
        assertNull("outer version", PushEnvelope.open(with("v", "2"), keys, now));
        assertNull("outer version type", PushEnvelope.open(with("v", "1.0"), keys, now));
        assertNull("length", PushEnvelope.open(with("c", c.substring(0, c.length() - 4)), keys, now));
        assertNull("alphabet", PushEnvelope.open(with("c", c.replace('-', '+')), keys, now));
        assertNull("padding", PushEnvelope.open(with("k", envelope().get("k").substring(0, 10) + "="), keys, now));
        Map<String, String> missing = envelope();
        missing.remove("k");
        assertNull("missing kid", PushEnvelope.open(missing, keys, now));
        assertNull(PushEnvelope.open(null, keys, now));
        assertNull(PushEnvelope.open(envelope(), null, now));
    }

    // The AAD is "moa-push-v1" || kid. A server or client that drops the kid
    // (or the label) from the AAD must not interoperate.
    @Test
    public void aadBindsLabelAndKid() throws Exception {
        String json = "{\"v\":1,\"id\":\"x\",\"exp\":1790043200,\"d\":\"home\",\"k\":\"done\",\"lvl\":\"active\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}";
        assertNotNull(PushEnvelope.open(seal(json, PushEnvelope.PADDED_SIZE, PushEnvelope.concat(PushEnvelope.AAD_LABEL, keys.kid)), keys, now));
        assertNull(PushEnvelope.open(seal(json, PushEnvelope.PADDED_SIZE, PushEnvelope.AAD_LABEL), keys, now));
        assertNull(PushEnvelope.open(seal(json, PushEnvelope.PADDED_SIZE, keys.kid), keys, now));
        assertNull(PushEnvelope.open(seal(json, PushEnvelope.PADDED_SIZE, new byte[0]), keys, now));
    }

    // The cases below need plaintexts the vectors do not carry, so they are
    // sealed here with the vector key exactly as the server would.

    @Test
    public void rejectsUnknownInnerVersion() throws Exception {
        assertNull(PushEnvelope.open(seal("{\"v\":2,\"id\":\"x\",\"exp\":1790043200,\"d\":\"home\",\"k\":\"done\",\"lvl\":\"active\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}"), keys, now));
        assertNull(PushEnvelope.open(seal("{\"v\":\"1\",\"id\":\"x\",\"exp\":1790043200,\"d\":\"home\",\"k\":\"done\",\"lvl\":\"active\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}"), keys, now));
    }

    @Test
    public void rejectsWrongPadding() throws Exception {
        String json = "{\"v\":1,\"id\":\"x\",\"exp\":1790043200,\"d\":\"home\",\"k\":\"done\",\"lvl\":\"active\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}";
        byte[] aad = PushEnvelope.concat(PushEnvelope.AAD_LABEL, keys.kid);
        assertNull("2047", PushEnvelope.open(seal(json, 2047, aad), keys, now));
        assertNull("2049", PushEnvelope.open(seal(json, 2049, aad), keys, now));
    }

    @Test
    public void rejectsMissingFields() throws Exception {
        assertNull("no title", PushEnvelope.open(seal("{\"v\":1,\"exp\":1790043200,\"d\":\"home\",\"lvl\":\"active\",\"th\":\"t\",\"b\":\"b\"}"), keys, now));
        assertNull("no exp", PushEnvelope.open(seal("{\"v\":1,\"d\":\"home\",\"lvl\":\"active\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}"), keys, now));
        assertNull("not json", PushEnvelope.open(seal("not json"), keys, now));
    }

    @Test
    public void invalidSessionIdBecomesHome() throws Exception {
        PushEnvelope.Content content = PushEnvelope.open(seal("{\"v\":1,\"id\":\"x\",\"exp\":1790043200,\"d\":\"session\",\"s\":\"../evil?x=1\",\"k\":\"ask\",\"lvl\":\"passive\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}"), keys, now);
        assertNotNull(content);
        assertEquals(PushEnvelope.Destination.HOME, content.destination);
        assertEquals(PushEnvelope.Level.PASSIVE, content.level);
        assertEquals("https://moa.example/", content.destination.url("https://moa.example"));
    }

    @Test
    public void inboxDestinationAndUnknownLevel() throws Exception {
        PushEnvelope.Content content = PushEnvelope.open(seal("{\"v\":1,\"id\":\"x\",\"exp\":1790043200,\"d\":\"inbox\",\"k\":\"event\",\"lvl\":\"shiny\",\"th\":\"t\",\"t\":\"a\",\"b\":\"b\"}"), keys, now);
        assertNotNull(content);
        assertEquals(PushEnvelope.Destination.INBOX, content.destination);
        // An unknown level from a newer server still sounds.
        assertEquals(PushEnvelope.Level.ACTIVE, content.level);
        assertEquals("https://moa.example/?inbox=1", content.destination.url("https://moa.example"));
        assertNull("destination never leaves an https origin", content.destination.url("http://moa.example"));
    }

    @Test
    public void base64UrlIsStrict() {
        assertNull(PushEnvelope.base64UrlDecode("ab+c"));
        assertNull(PushEnvelope.base64UrlDecode("abc="));
        assertNull(PushEnvelope.base64UrlDecode("a"));
        assertTrue(Arrays.equals(new byte[] { (byte) 0xfb, (byte) 0xff }, PushEnvelope.base64UrlDecode("-_8")));
    }

    private static Map<String, String> seal(String json) throws Exception {
        return seal(json, PushEnvelope.PADDED_SIZE, PushEnvelope.concat(PushEnvelope.AAD_LABEL, keys.kid));
    }

    private static Map<String, String> seal(String json, int size, byte[] aad) throws Exception {
        byte[] text = json.getBytes(StandardCharsets.UTF_8);
        byte[] padded = Arrays.copyOf(text, size);
        Arrays.fill(padded, text.length, size, (byte) 0x20);
        byte[] nonce = new byte[12];
        new SecureRandom().nextBytes(nonce);
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.ENCRYPT_MODE, new SecretKeySpec(keys.enc, "AES"), new GCMParameterSpec(128, nonce));
        cipher.updateAAD(aad);
        byte[] sealed = PushEnvelope.concat(nonce, cipher.doFinal(padded));
        Map<String, String> data = new HashMap<>();
        data.put("v", "1");
        data.put("k", PushEnvelope.base64UrlEncode(keys.kid));
        data.put("c", PushEnvelope.base64UrlEncode(sealed));
        return data;
    }

    private static Map<String, String> with(String key, String value) throws Exception {
        Map<String, String> data = envelope();
        data.put(key, value);
        return data;
    }

    private static String flip(String text, int offset) {
        char[] chars = text.toCharArray();
        chars[offset] = chars[offset] == 'A' ? 'B' : 'A';
        return new String(chars);
    }

    private static String hex(byte[] data) {
        StringBuilder out = new StringBuilder();
        for (byte b : data) out.append(String.format("%02x", b));
        return out.toString();
    }
}
