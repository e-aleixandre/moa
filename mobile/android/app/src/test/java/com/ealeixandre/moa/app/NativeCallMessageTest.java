package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

// What the injected window.MoaNativeCall posts, one JSON object per call
// (iOS: the messageHandlers body).
public class NativeCallMessageTest {

    @Test
    public void parsesOpen() {
        NativeCallMessage m = NativeCallMessage.parse(
            "{\"op\":\"open\",\"id\":\"n1\",\"url\":\"wss://h/api/voice/calls/c/ws\",\"name\":\"Moa\",\"sessionId\":\"s1\"}");
        assertNotNull(m);
        assertEquals("open", m.op);
        assertEquals("n1", m.id);
        assertEquals("wss://h/api/voice/calls/c/ws", m.url);
        assertEquals("Moa", m.name);
        assertEquals("s1", m.sessionId);
    }

    // The page says whether the name is an owner's; a conversation title is
    // never shown on a lock screen, so anything else is the app's own name.
    @Test
    public void onlyAnOwnersNameIsShownOnTheLockScreen() {
        NativeCallMessage owner = NativeCallMessage.parse("{\"op\":\"open\",\"id\":\"n1\",\"name\":\"Atlas\",\"owner\":true}");
        assertEquals("Atlas", owner.displayName());
        NativeCallMessage title = NativeCallMessage.parse("{\"op\":\"open\",\"id\":\"n1\",\"name\":\"PRIVATE: diagnosis\",\"owner\":false}");
        assertEquals("moa", title.displayName());
        // An older page does not say: not an owner.
        NativeCallMessage older = NativeCallMessage.parse("{\"op\":\"open\",\"id\":\"n1\",\"name\":\"PRIVATE: diagnosis\"}");
        assertEquals("moa", older.displayName());
        // Only a real boolean counts.
        NativeCallMessage text = NativeCallMessage.parse("{\"op\":\"open\",\"id\":\"n1\",\"name\":\"X\",\"owner\":\"true\"}");
        assertEquals("moa", text.displayName());
    }

    @Test
    public void theSystemCallIsStartedWithTheDisplayName() {
        NativeCallManagerTest.FakeHost host = new NativeCallManagerTest.FakeHost();
        NativeCallManager manager = new NativeCallManager(host);
        NativeCallMessage.parse("{\"op\":\"open\",\"id\":\"n1\",\"url\":\"" + NativeCallManagerTest.URL
            + "\",\"name\":\"PRIVATE: diagnosis\",\"sessionId\":\"s\"}").applyTo(manager);
        assertEquals("start moa", host.system.log.get(0));
    }

    @Test
    public void openDefaultsLikeIos() {
        NativeCallMessage m = NativeCallMessage.parse("{\"op\":\"open\",\"id\":\"n1\"}");
        assertEquals("", m.url);
        assertEquals("moa", m.name);
        assertEquals("", m.sessionId);
    }

    @Test
    public void parsesSendAndMute() {
        assertEquals("{\"type\":\"hangup\"}", NativeCallMessage.parse("{\"op\":\"send\",\"id\":\"n1\",\"text\":\"{\\\"type\\\":\\\"hangup\\\"}\"}").text);
        assertTrue(NativeCallMessage.parse("{\"op\":\"setMuted\",\"id\":\"n1\",\"muted\":true}").muted);
        assertFalse(NativeCallMessage.parse("{\"op\":\"setMuted\",\"id\":\"n1\",\"muted\":false}").muted);
        assertFalse(NativeCallMessage.parse("{\"op\":\"setMuted\",\"id\":\"n1\"}").muted);
        assertFalse(NativeCallMessage.parse("{\"op\":\"setMuted\",\"id\":\"n1\",\"muted\":\"true\"}").muted);
    }

    @Test
    public void activeCarriesAnEmptyId() {
        NativeCallMessage m = NativeCallMessage.parse("{\"op\":\"active\",\"id\":\"\"}");
        assertEquals("active", m.op);
        assertEquals("", m.id);
    }

    @Test
    public void refusesWhatIsNotAnOpWithAnId() {
        assertNull(NativeCallMessage.parse(null));
        assertNull(NativeCallMessage.parse(""));
        assertNull(NativeCallMessage.parse("not json"));
        assertNull(NativeCallMessage.parse("[]"));
        assertNull(NativeCallMessage.parse("{\"id\":\"n1\"}"));
        assertNull(NativeCallMessage.parse("{\"op\":\"open\"}"));
        assertNull(NativeCallMessage.parse("{\"op\":1,\"id\":\"n1\"}"));
        assertNull(NativeCallMessage.parse("{\"op\":\"open\",\"id\":5}"));
    }

    @Test
    public void everyOpIsKnownToThePolicyAndNothingElseIs() {
        for (String op : new String[] {"open", "send", "close", "active", "startAudio", "stopAudio", "setMuted"}) {
            assertTrue(op, OriginPolicy.mayCall("call", op, OriginPolicy.Caller.PAIRED));
            assertFalse(op, OriginPolicy.mayCall("call", op, OriginPolicy.Caller.LOCAL));
            assertFalse(op, OriginPolicy.mayCall("call", op, OriginPolicy.Caller.OTHER));
        }
        assertFalse(OriginPolicy.mayCall("call", "__event", OriginPolicy.Caller.PAIRED));
        assertFalse(OriginPolicy.mayCall("call", "reset", OriginPolicy.Caller.PAIRED));
        assertFalse(OriginPolicy.mayCall("call", null, OriginPolicy.Caller.PAIRED));
    }
}
