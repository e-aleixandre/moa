package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;

import org.junit.Test;

// The same acceptance rules as iOS callURL: only the paired server's own
// /api/voice/calls/{id}/ws over wss, nothing else.
public class CallUrlTest {

    private static final String PAIRED = "https://moa.example.ts.net:8443";
    private static final String GOOD = "wss://moa.example.ts.net:8443/api/voice/calls/call_abc/ws";

    @Test
    public void acceptsThePairedServersCallSocket() {
        CallUrl url = CallUrl.validate(GOOD, PAIRED);
        assertNotNull(url);
        assertEquals(GOOD, url.url);
        assertEquals("call_abc", url.callId);
    }

    @Test
    public void comparesTheOriginNotAPrefix() {
        assertNotNull(CallUrl.validate("wss://MOA.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNotNull(CallUrl.validate("wss://moa.example.ts.net/api/voice/calls/c/ws", "https://moa.example.ts.net"));
        assertNotNull(CallUrl.validate("wss://moa.example.ts.net:443/api/voice/calls/c/ws", "https://moa.example.ts.net"));
        assertNull(CallUrl.validate("wss://moa.example.ts.net:8444/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("wss://moa.example.ts.net/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("wss://evil.moa.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("wss://moa.example.ts.net.evil.com:8443/api/voice/calls/c/ws", PAIRED));
    }

    @Test
    public void refusesWithoutAPairedOrigin() {
        assertNull(CallUrl.validate(GOOD, null));
        assertNull(CallUrl.validate(GOOD, ""));
        assertNull(CallUrl.validate(GOOD, "http://moa.example.ts.net:8443"));
    }

    @Test
    public void refusesOtherSchemes() {
        assertNull(CallUrl.validate("ws://moa.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("https://moa.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("//moa.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("/api/voice/calls/c/ws", PAIRED));
    }

    @Test
    public void refusesUserInfoQueryAndFragment() {
        assertNull(CallUrl.validate("wss://user@moa.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("wss://user:pw@moa.example.ts.net:8443/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate(GOOD + "?client_note=x", PAIRED));
        assertNull(CallUrl.validate(GOOD + "?", PAIRED));
        assertNull(CallUrl.validate(GOOD + "#frag", PAIRED));
        assertNull(CallUrl.validate(GOOD + "#", PAIRED));
    }

    @Test
    public void refusesAnyOtherPath() {
        String base = "wss://moa.example.ts.net:8443";
        assertNull(CallUrl.validate(base, PAIRED));
        assertNull(CallUrl.validate(base + "/", PAIRED));
        assertNull(CallUrl.validate(base + "/api/voice/calls//ws", PAIRED));
        assertNull(CallUrl.validate(base + "/api/voice/calls/c", PAIRED));
        assertNull(CallUrl.validate(base + "/api/voice/calls/c/ws/", PAIRED));
        assertNull(CallUrl.validate(base + "/api/voice/calls/c/ws/x", PAIRED));
        assertNull(CallUrl.validate(base + "/api/voice/calls/a/b/ws", PAIRED));
        assertNull(CallUrl.validate(base + "/api/voice/call/c/ws", PAIRED));
        assertNull(CallUrl.validate(base + "/API/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate(base + "/x/../api/voice/calls/c/ws", PAIRED));
        // An encoded slash would decode into another segment.
        assertNull(CallUrl.validate(base + "/api/voice/calls/a%2Fb/ws", PAIRED));
    }

    @Test
    public void refusesGarbage() {
        assertNull(CallUrl.validate(null, PAIRED));
        assertNull(CallUrl.validate("", PAIRED));
        assertNull(CallUrl.validate("wss://moa example/api/voice/calls/c/ws", PAIRED));
        assertNull(CallUrl.validate("not a url", PAIRED));
    }
}
