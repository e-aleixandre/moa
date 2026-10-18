package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertTrue;

import java.util.List;
import org.junit.Before;
import org.junit.Test;

// An answered invitation joins the call the server opened for it, with the
// Moa-Voice key and no page (iOS startIncoming): audio starts by itself once
// the system call is ready, and the page that opens later adopts it.
public class NativeIncomingCallTest {

    static final String ORIGIN = NativeCallManagerTest.ORIGIN;
    static final String URL = NativeCallManagerTest.URL;
    static final String VOICE = "Moa-Voice dev_1.key";

    private NativeCallManagerTest.FakeHost host;
    private NativeCallManager manager;

    @Before
    public void setUp() {
        host = new NativeCallManagerTest.FakeHost();
        manager = new NativeCallManager(host);
    }

    private boolean join() {
        return manager.openIncoming("Moa", "sess_1", "call_1", ORIGIN, URL, VOICE);
    }

    @Test
    public void joinsWithTheVoiceCredentialAndTheIncomingSystemCall() {
        assertTrue(join());
        assertEquals(URL, host.socket().url);
        assertEquals(VOICE, host.socket().authorization);
        assertEquals(List.of("startIncoming Moa"), host.system.log);
        assertTrue(manager.hasCall());
    }

    @Test
    public void refusesAnotherServersUrlOrAnUnpairedApp() {
        assertFalse(manager.openIncoming("Moa", "s", "c", ORIGIN, "wss://evil.example/api/voice/calls/c/ws", VOICE));
        assertTrue(host.sockets.isEmpty());
        host.origin = null;
        assertFalse(join());
        host.origin = "https://other.example";
        assertFalse(join());
        assertTrue(host.sockets.isEmpty());
        assertFalse(manager.hasCall());
        assertTrue(host.system.log.isEmpty());
    }

    @Test
    public void refusesWhileAnotherCallIsOn() {
        manager.open("n1", URL, "Moa", "s");
        assertFalse(join());
        assertEquals(1, host.sockets.size());
    }

    @Test
    public void theMicrophoneOpensByItselfOnceTheSystemCallIsReady() {
        join();
        host.socket().events.opened();
        assertTrue(host.audios.isEmpty());
        host.system.listener.ready();
        assertEquals(1, host.audios.size());
        assertTrue(host.audio().log.contains("start"));
        assertTrue(host.system.log.contains("microphone"));
        assertEquals("granted", host.mic);
    }

    @Test
    public void aPageThatOpensLaterAdoptsTheRunningCall() {
        join();
        host.socket().events.opened();
        host.system.listener.ready();
        host.events.clear();
        manager.reportActive();
        NativeCallManagerTest.Event active = host.last("active");
        assertNotNull(active.data);
        String id = (String) ((java.util.Map<?, ?>) active.data).get("id");
        assertEquals("call_1", ((java.util.Map<?, ?>) active.data).get("callId"));
        assertEquals("sess_1", ((java.util.Map<?, ?>) active.data).get("sessionId"));
        manager.startAudio(id);
        assertEquals(1, host.audios.size());
        assertEquals(id + ":audio:ok", host.last("audio").toString());
        manager.send(id, "{\"type\":\"ping\"}");
        assertTrue(host.socket().texts.contains("{\"type\":\"ping\"}"));
    }

    @Test
    public void withoutMicrophonePermissionTheCallEndsAsMicNotGrantedAndTheServerIsToldToHangUp() {
        host.mic = "denied";
        assertTrue(join());
        host.socket().events.opened();
        host.system.listener.ready();
        assertTrue(host.audios.isEmpty());
        assertEquals(1000, host.socket().closeCode);
        assertEquals("native:mic_not_granted mic=denied", host.socket().closeReason);
        assertEquals(List.of(ORIGIN + " call_1 " + VOICE), host.hangups);
        assertFalse(manager.hasCall());
        assertTrue(host.system.log.contains("end"));
    }

    @Test
    public void withoutPermissionTheServerIsToldEvenIfTheSocketNeverOpens() {
        host.mic = "undetermined";
        join();
        host.socket().events.failed("SocketTimeoutException");
        assertEquals(List.of(ORIGIN + " call_1 " + VOICE), host.hangups);
        assertFalse(manager.hasCall());
    }

    @Test
    public void aCallWithTheMicrophoneNeverAsksTheServerToHangUpByItself() {
        join();
        host.socket().events.opened();
        host.system.listener.ready();
        host.socket().events.closed(1000);
        assertTrue(host.hangups.isEmpty());
        assertFalse(manager.hasCall());
    }

    @Test
    public void hangingUpFromTheSystemEndsItLikeAnyCall() {
        join();
        host.socket().events.opened();
        host.system.listener.ready();
        host.system.listener.hangUp();
        assertTrue(host.socket().texts.contains(CallProtocol.HANGUP_FRAME));
    }

    @Test
    public void changingThePairingEndsTheIncomingCall() {
        join();
        host.socket().events.opened();
        host.origin = "https://another.example";
        manager.bindingChanged();
        assertFalse(manager.hasCall());
        assertEquals("native:unpaired mic=granted", host.socket().closeReason);
    }

    @Test
    public void aSystemCallThatCannotBeSetUpEndsIt() {
        join();
        host.system.listener.failed("refused");
        assertFalse(manager.hasCall());
    }
}
