package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

// The last fence before every private navigation (NavigationGate). Its
// callers decide a URL at one moment and navigate later (a deferred
// window.MoaNavigation result, a cold-start link held until the paired page
// loads, a transport retry): the gate is asked again at that later moment.
public class NavigationGateTest {

    private static final String ORIGIN = "https://moa.example";
    private static final String SESSION = ORIGIN + "/?session=abc";

    private boolean valid = true;
    private String origin = ORIGIN;
    private int generation;
    private int pairings;

    private final NavigationGate gate = new NavigationGate(new NavigationGate.State() {
        @Override
        public boolean knownValid() {
            return valid;
        }

        @Override
        public String knownOrigin() {
            return origin;
        }

        @Override
        public int generation() {
            return generation;
        }
    });

    private boolean admit(String url, int decidedIn) {
        return gate.admit(url, decidedIn, () -> pairings++);
    }

    @Test
    public void currentNavigationIsAdmitted() {
        assertTrue(admit(SESSION, 0));
        assertEquals(0, pairings);
    }

    // Re-review e2 case 2: the JS fallback was decided while valid; the
    // credential expired (clock only) before its callback loads the URL.
    @Test
    public void navigationDecidedWhileValidPairsOnceTheCredentialExpired() {
        int decidedIn = gate.generation();
        valid = false;
        assertFalse(admit(SESSION, decidedIn));
        assertEquals(1, pairings);
    }

    // Unpaired, the page is already the local one: nothing to do, and a
    // reload would interrupt a pairing in progress.
    @Test
    public void unpairedGoesNowhere() {
        valid = false;
        origin = null;
        assertFalse(admit(SESSION, 0));
        assertEquals(0, pairings);
    }

    @Test
    public void navigationFromAnEndedGenerationGoesNowhere() {
        generation = 1;
        assertFalse(admit(SESSION, 0));
        assertEquals(0, pairings);
    }

    @Test
    public void navigationForAnotherPairingGoesNowhere() {
        origin = "https://other.example";
        assertFalse(admit(SESSION, 0));
        assertEquals(0, pairings);
    }

    @Test
    public void urlOutsideThePairedOriginGoesNowhere() {
        assertFalse(admit("https://elsewhere.example/?session=abc", 0));
        assertFalse(admit(null, 0));
        assertEquals(0, pairings);
    }
}
