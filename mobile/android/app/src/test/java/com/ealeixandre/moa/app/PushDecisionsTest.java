package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.Map;
import org.junit.Test;

// Message classification, channels, replacement tags, the visible-session
// rule, relay answers, the session cookie and the Back decision.
public class PushDecisionsTest {

    private static Map<String, String> map(String... pairs) {
        Map<String, String> data = new HashMap<>();
        for (int i = 0; i < pairs.length; i += 2) data.put(pairs[i], pairs[i + 1]);
        return data;
    }

    private static PushEnvelope.Content content(String id, PushEnvelope.Destination destination, PushEnvelope.Level level) {
        return new PushEnvelope.Content(id, destination, level, "th", "t", "b");
    }

    @Test
    public void classifiesRelayMessages() {
        assertEquals(PushMessages.Kind.CHALLENGE, PushMessages.classify(map("r", "AcDB")));
        assertEquals(PushMessages.Kind.ENVELOPE, PushMessages.classify(map("v", "1", "k", "x", "c", "y")));
        assertEquals(PushMessages.Kind.ENVELOPE, PushMessages.classify(map("v", "1", "k", "x", "c", "y", "col", "z")));
        // A challenge field next to an envelope is never treated as a challenge.
        assertEquals(PushMessages.Kind.ENVELOPE, PushMessages.classify(map("r", "AcDB", "c", "y")));
        assertEquals(PushMessages.Kind.IGNORE, PushMessages.classify(map("title", "hi")));
        assertEquals(PushMessages.Kind.IGNORE, PushMessages.classify(null));
    }

    @Test
    public void mapsLevelsToTheThreeChannels() {
        assertEquals("moa_urgent", PushMessages.channelFor(PushEnvelope.Level.URGENT));
        assertEquals("moa_active", PushMessages.channelFor(PushEnvelope.Level.ACTIVE));
        assertEquals("moa_passive", PushMessages.channelFor(PushEnvelope.Level.PASSIVE));
        // Undecryptable: the active channel, which sounds.
        assertEquals("moa_active", PushMessages.fallbackChannel());
        assertEquals(4, PushMessages.importanceFor("moa_urgent")); // IMPORTANCE_HIGH: heads-up
        assertEquals(3, PushMessages.importanceFor("moa_active")); // IMPORTANCE_DEFAULT: sound
        assertEquals(2, PushMessages.importanceFor("moa_passive")); // IMPORTANCE_LOW: silent
    }

    @Test
    public void replacesByCollapseIdElseByEnvelopeId() {
        PushEnvelope.Content c = content("oKGio6SlpqeoqaqrrK2urw", PushEnvelope.Destination.HOME, PushEnvelope.Level.PASSIVE);
        assertEquals("col:z4jJioq42N4PkWJeU2NR8j", PushMessages.tagFor(map("col", "z4jJioq42N4PkWJeU2NR8j", "c", "gIGCg4SFhoeIiYqLZzpK"), c));
        assertEquals("id:oKGio6SlpqeoqaqrrK2urw", PushMessages.tagFor(map("c", "gIGCg4SFhoeIiYqLZzpK"), c));
        // A collapse id the server could not have produced is not trusted.
        assertEquals("id:oKGio6SlpqeoqaqrrK2urw", PushMessages.tagFor(map("col", "../../x", "c", "gIGCg4SFhoeIiYqLZzpK"), c));
        // Fallback (no content): the nonce prefix keeps distinct alerts apart.
        assertEquals("c:gIGCg4SFhoeIiYqL", PushMessages.tagFor(map("c", "gIGCg4SFhoeIiYqLZzpK"), null));
    }

    @Test
    public void suppressesOnlyTheVisibleSessionInTheForeground() {
        PushEnvelope.Content s1 = content("x", PushEnvelope.Destination.session("sess_1"), PushEnvelope.Level.URGENT);
        assertTrue(PushMessages.suppress(s1, "sess_1", true));
        assertFalse(PushMessages.suppress(s1, "sess_1", false));
        assertFalse(PushMessages.suppress(s1, "sess_2", true));
        assertFalse(PushMessages.suppress(s1, null, true));
        assertFalse(PushMessages.suppress(content("x", PushEnvelope.Destination.INBOX, PushEnvelope.Level.ACTIVE), "sess_1", true));
        // Undecryptable messages are always shown.
        assertFalse(PushMessages.suppress(null, "sess_1", true));
    }

    @Test
    public void validatesChallenges() {
        assertTrue(PushMessages.isChallenge("AcDBwsPExcbHyMnKy8rF"));
        assertFalse(PushMessages.isChallenge(""));
        assertFalse(PushMessages.isChallenge("not+base64"));
        assertFalse(PushMessages.isChallenge(null));
        char[] longChallenge = new char[2049];
        Arrays.fill(longChallenge, 'A');
        assertFalse(PushMessages.isChallenge(new String(longChallenge)));
    }

    @Test
    public void registersWithTheFcmProvider() throws Exception {
        org.json.JSONObject body = RelayProtocol.registerBody("tok", "key");
        assertEquals("tok", body.getString("token"));
        assertEquals("production", body.getString("env"));
        assertEquals("key", body.getString("send_key"));
        assertEquals("fcm", body.getString("provider"));
        assertEquals(4, body.length());
    }

    // Review I2: only the destination being registered is accepted, and a
    // relay that does not say which one it confirmed is refused.
    @Test
    public void acceptsAConfirmationOnlyForThePendingDestination() {
        long now = 1_790_000_000L;
        String handle = "AcDBwsPExcbHyMnKy82V3KcNiH8aejKiNYC0kkcSakI048p6Oqf3";
        String good = "{\"handle\":\"" + handle + "\",\"expires_at\":" + (now + 86400) + ",\"token\":\"aa\",\"env\":\"production\",\"provider\":\"fcm\"}";
        assertTrue(RelayProtocol.confirms(RelayProtocol.parseHandle(good, now), "aa", "production"));
        assertFalse(RelayProtocol.confirms(RelayProtocol.parseHandle(good, now), "bb", "production"));
        assertFalse(RelayProtocol.confirms(RelayProtocol.parseHandle(good.replace("production", "sandbox"), now), "aa", "production"));
        assertFalse(RelayProtocol.confirms(RelayProtocol.parseHandle(good.replace("\"fcm\"", "\"apns\""), now), "aa", "production"));
        assertFalse(RelayProtocol.confirms(RelayProtocol.parseHandle(good.replace(",\"provider\":\"fcm\"", ""), now), "aa", "production"));
        assertFalse(RelayProtocol.confirms(RelayProtocol.parseHandle(good.replace(",\"token\":\"aa\"", ""), now), "aa", "production"));
        assertFalse(RelayProtocol.confirms(null, "aa", "production"));
    }

    @Test
    public void parsesOnlyPlausibleHandles() {
        long now = 1_790_000_000L;
        String handle = "AcDBwsPExcbHyMnKy82V3KcNiH8aejKiNYC0kkcSakI048p6Oqf3";
        assertNotNull(RelayProtocol.parseHandle("{\"handle\":\"" + handle + "\",\"expires_at\":" + (now + 90L * 86400) + "}", now));
        assertNull("expired", RelayProtocol.parseHandle("{\"handle\":\"" + handle + "\",\"expires_at\":" + now + "}", now));
        assertNull("too long-lived", RelayProtocol.parseHandle("{\"handle\":\"" + handle + "\",\"expires_at\":" + (now + 92L * 86400) + "}", now));
        assertNull("short", RelayProtocol.parseHandle("{\"handle\":\"abc\",\"expires_at\":" + (now + 60) + "}", now));
        assertNull("alphabet", RelayProtocol.parseHandle("{\"handle\":\"" + handle + "+\",\"expires_at\":" + (now + 60) + "}", now));
        assertNull("not json", RelayProtocol.parseHandle("<html>", now));
    }

    @Test
    public void acceptsOnlyTheHostOnlyHttpOnlySessionCookie() {
        String good = "__Host-moa_device=v1.dev.123.abc.def; Path=/; Expires=Wed, 01 Jan 2099 00:00:00 GMT; Max-Age=86400; HttpOnly; Secure; SameSite=Strict";
        assertEquals(good, SessionCookie.select(Arrays.asList("other=1; Path=/", good)));
        assertNull(SessionCookie.select(Collections.singletonList(good.replace("; HttpOnly", ""))));
        assertNull(SessionCookie.select(Collections.singletonList(good.replace("; Secure", ""))));
        assertNull(SessionCookie.select(Collections.singletonList(good.replace("Path=/", "Path=/api"))));
        assertNull(SessionCookie.select(Collections.singletonList(good + "; Domain=moa.example")));
        assertNull(SessionCookie.select(Collections.singletonList(good.replace("Max-Age=86400", "Max-Age=0"))));
        assertNull(SessionCookie.select(Collections.singletonList(good.replace("SameSite=Strict", "SameSite=Lax"))));
        assertNull(SessionCookie.select(Collections.singletonList(good.replace("__Host-moa_device", "moa_device"))));
        assertNull(SessionCookie.select(null));
        assertTrue(SessionCookie.expired().startsWith("__Host-moa_device=;"));
        assertTrue(SessionCookie.expired().contains("Max-Age=0"));
    }

    @Test
    public void backMinimizesUnlessThePageHandledIt() {
        assertFalse(BackDecision.shouldMinimize("true"));
        assertTrue(BackDecision.shouldMinimize("false"));
        assertTrue(BackDecision.shouldMinimize("null"));
        assertTrue(BackDecision.shouldMinimize("{}"));
        assertTrue(BackDecision.shouldMinimize(null));
        // The page's handler must return a literal true, synchronously.
        assertTrue(BackDecision.SCRIPT.contains("window.MoaHandleBack() === true"));
    }

    @Test
    public void inPlaceNavigationPassesTheIdAsALiteral() {
        String script = PushMessages.openInPlaceScript(PushEnvelope.Destination.session("sess_1"), "https://moa.example/?session=sess_1");
        assertTrue(script.contains("nav.openSession(\"sess_1\")"));
        assertTrue(script.contains("location.assign(url)"));
        String inbox = PushMessages.openInPlaceScript(PushEnvelope.Destination.INBOX, "https://moa.example/?inbox=1");
        assertTrue(inbox.contains("nav.openInbox()"));
    }
}
