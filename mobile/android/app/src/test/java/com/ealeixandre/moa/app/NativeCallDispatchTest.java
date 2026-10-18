package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Before;
import org.junit.Test;

// The page's messages reach the manager as the iOS handler's switch does.
public class NativeCallDispatchTest {

    private NativeCallManagerTest.FakeHost host;
    private NativeCallManager manager;

    @Before
    public void setUp() {
        host = new NativeCallManagerTest.FakeHost();
        manager = new NativeCallManager(host);
    }

    private void post(String json) {
        NativeCallMessage.parse(json).applyTo(manager);
    }

    @Test
    public void openStartsTheCall() {
        post("{\"op\":\"open\",\"id\":\"n1\",\"url\":\"" + NativeCallManagerTest.URL + "\",\"name\":\"Moa\",\"sessionId\":\"s\"}");
        assertEquals(NativeCallManagerTest.URL, host.socket().url);
        assertTrue(manager.hasCall());
    }

    @Test
    public void theRestOfTheOpsReachTheCall() {
        post("{\"op\":\"open\",\"id\":\"n1\",\"url\":\"" + NativeCallManagerTest.URL + "\",\"name\":\"Moa\",\"sessionId\":\"s\"}");
        host.socket().events.opened();
        post("{\"op\":\"send\",\"id\":\"n1\",\"text\":\"{\\\"type\\\":\\\"ping\\\"}\"}");
        assertEquals("{\"type\":\"ping\"}", host.socket().texts.get(0));
        post("{\"op\":\"setMuted\",\"id\":\"n1\",\"muted\":true}");
        assertEquals("{\"type\":\"mute\",\"muted\":true}", host.socket().texts.get(1));
        post("{\"op\":\"active\",\"id\":\"\"}");
        assertEquals("active", host.last("active").type);
        host.system.listener.ready();
        post("{\"op\":\"startAudio\",\"id\":\"n1\"}");
        assertEquals("audio", host.last("audio").type);
        assertEquals(1, host.audios.size());
        post("{\"op\":\"stopAudio\",\"id\":\"n1\"}");
        assertEquals("stop", host.audio().log.get(host.audio().log.size() - 1));
        post("{\"op\":\"close\",\"id\":\"n1\"}");
        assertFalse(manager.hasCall());
    }

    @Test
    public void aSendWithoutTextAndAnUnknownOpDoNothing() {
        post("{\"op\":\"open\",\"id\":\"n1\",\"url\":\"" + NativeCallManagerTest.URL + "\"}");
        host.socket().events.opened();
        post("{\"op\":\"send\",\"id\":\"n1\"}");
        post("{\"op\":\"reset\",\"id\":\"n1\"}");
        assertTrue(host.socket().texts.isEmpty());
        assertTrue(manager.hasCall());
    }
}
