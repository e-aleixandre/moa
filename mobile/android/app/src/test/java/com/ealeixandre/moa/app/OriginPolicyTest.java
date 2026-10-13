package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import com.ealeixandre.moa.app.OriginPolicy.Caller;
import com.ealeixandre.moa.app.OriginPolicy.Navigation;
import org.junit.Test;

public class OriginPolicyTest {

    private static final String PAIRED = "https://moa.example.ts.net:8443";

    @Test
    public void normalizesOnlyBareHttpsOrigins() {
        assertEquals("https://moa.example", OriginPolicy.normalizeOrigin("https://MOA.example"));
        assertEquals("https://moa.example", OriginPolicy.normalizeOrigin("https://moa.example/"));
        assertEquals("https://moa.example", OriginPolicy.normalizeOrigin("https://moa.example:443"));
        assertEquals("https://moa.example:8443", OriginPolicy.normalizeOrigin("https://moa.example:8443"));
        assertNull(OriginPolicy.normalizeOrigin("http://moa.example"));
        assertNull(OriginPolicy.normalizeOrigin("https://user:pw@moa.example"));
        assertNull(OriginPolicy.normalizeOrigin("https://moa.example/path"));
        assertNull(OriginPolicy.normalizeOrigin("https://moa.example/?q=1"));
        assertNull(OriginPolicy.normalizeOrigin("https://moa.example/#x"));
        assertNull(OriginPolicy.normalizeOrigin("capacitor://localhost"));
        assertNull(OriginPolicy.normalizeOrigin(""));
        assertNull(OriginPolicy.normalizeOrigin(null));
    }

    @Test
    public void classifiesCallersByExactOrigin() {
        assertEquals(Caller.LOCAL, OriginPolicy.classify("https://localhost", PAIRED));
        assertEquals(Caller.PAIRED, OriginPolicy.classify(PAIRED, PAIRED));
        assertEquals(Caller.PAIRED, OriginPolicy.classify("https://MOA.example.ts.net:8443", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("https://moa.example.ts.net", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("https://moa.example.ts.net:8444", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("http://moa.example.ts.net:8443", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("https://evil.moa.example.ts.net:8443", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("https://localhost:8443", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("http://localhost", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("capacitor://localhost", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify("null", PAIRED));
        assertEquals(Caller.OTHER, OriginPolicy.classify(PAIRED, null));
        assertEquals(Caller.OTHER, OriginPolicy.classify(null, PAIRED));
    }

    // The iOS table: claim/authorize from the local page only, reauthorize
    // from the paired page only, reset from either; push from the paired
    // page only; the share inbox counted and bound from the local page and
    // read only by the paired page; nothing for any other origin.
    @Test
    public void authorizesBridgeMethodsLikeIos() {
        String[][] allowed = {
            { "auth", "claim", "LOCAL" },
            { "auth", "authorize", "LOCAL" },
            { "auth", "reauthorize", "PAIRED" },
            { "auth", "reset", "LOCAL" },
            { "auth", "reset", "PAIRED" },
            { "push", "status", "PAIRED" },
            { "push", "enable", "PAIRED" },
            { "push", "disable", "PAIRED" },
            { "push", "setVisibleSession", "PAIRED" },
            { "push", "openFallback", "PAIRED" },
            { "share", "status", "LOCAL" },
            { "share", "bindServer", "LOCAL" },
            { "share", "clearServer", "LOCAL" },
            { "share", "peek", "PAIRED" },
            { "share", "acknowledge", "PAIRED" },
        };
        String[][] methods = {
            { "auth", "claim" }, { "auth", "authorize" }, { "auth", "reauthorize" }, { "auth", "reset" },
            { "auth", "unpair" }, { "push", "status" }, { "push", "enable" }, { "push", "disable" },
            { "push", "setVisibleSession" }, { "push", "openFallback" }, { "push", "claim" }, { "share", "peek" },
            { "share", "status" }, { "share", "bindServer" }, { "share", "clearServer" },
            { "share", "acknowledge" }, { "share", "reset" },
        };
        int granted = 0;
        for (String[] method : methods) {
            for (Caller caller : Caller.values()) {
                boolean expected = false;
                for (String[] row : allowed) {
                    expected |= row[0].equals(method[0]) && row[1].equals(method[1]) && row[2].equals(caller.name());
                }
                assertEquals(method[0] + "." + method[1] + " from " + caller, expected,
                    OriginPolicy.mayCall(method[0], method[1], caller));
                if (expected) granted++;
            }
        }
        assertEquals(allowed.length, granted);
    }

    @Test
    public void keepsTheMainFrameOnThePairedAndLocalOrigins() {
        assertEquals(Navigation.WEBVIEW, OriginPolicy.navigation(PAIRED + "/?session=abc", PAIRED));
        assertEquals(Navigation.WEBVIEW, OriginPolicy.navigation("https://localhost/", PAIRED));
        assertEquals(Navigation.WEBVIEW, OriginPolicy.navigation("https://localhost/index.html?unpaired=1", null));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("https://moa.example.ts.net/", PAIRED));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("http://moa.example.ts.net:8443/", PAIRED));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("https://github.com/x", PAIRED));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation(PAIRED + "/", null));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("https://localhost:8080/", PAIRED));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("mailto:a@b.c", PAIRED));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("javascript:alert(1)", PAIRED));
        assertEquals(Navigation.EXTERNAL, OriginPolicy.navigation("file:///sdcard/x", PAIRED));
        assertEquals(Navigation.DEFAULT, OriginPolicy.navigation("blob:https://moa/x", PAIRED));
        assertEquals(Navigation.DEFAULT, OriginPolicy.navigation("data:text/plain,hi", PAIRED));
        assertEquals(Navigation.LOCAL_PAGE, OriginPolicy.navigation("capacitor://localhost", PAIRED));
    }

    @Test
    public void mapsTheIosLocalOriginToTheAndroidOne() {
        assertEquals("https://localhost/", OriginPolicy.localPageFor("capacitor://localhost"));
        assertEquals("https://localhost/?unpaired=1", OriginPolicy.localPageFor("capacitor://localhost/?unpaired=1"));
        assertNull(OriginPolicy.localPageFor("capacitor://evil.example/"));
        assertNull(OriginPolicy.localPageFor("capacitor://localhost:9/"));
        assertNull(OriginPolicy.localPageFor("https://localhost/"));
    }

    @Test
    public void relayUrlIsABareHttpsOrigin() {
        assertTrue(OriginPolicy.validRelayUrl("https://moa-push-relay.ealeixandre.workers.dev"));
        assertFalse(OriginPolicy.validRelayUrl("https://relay.example/"));
        assertFalse(OriginPolicy.validRelayUrl("https://relay.example/v1"));
        assertFalse(OriginPolicy.validRelayUrl("http://relay.example"));
        assertFalse(OriginPolicy.validRelayUrl("https://u@relay.example"));
        assertFalse(OriginPolicy.validRelayUrl(""));
    }
}
