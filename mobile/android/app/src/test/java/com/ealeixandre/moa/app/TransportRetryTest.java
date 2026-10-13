package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import org.junit.Test;

// Re-review bb case 1: a transport retry of the paired page, scheduled
// before an expired tap, must not replace the pairing page the tap loads.
// The host plays the main-thread Handler: timers run only when fired.
public class TransportRetryTest {

    private static final String REMOTE = "https://moa.example/?session=old_private_session";
    private static final String PAIRING = "https://localhost/?unpaired=1";

    private final List<Runnable> timers = new ArrayList<>();
    private final List<Long> delays = new ArrayList<>();
    private final List<String> loads = new ArrayList<>();
    private boolean paired = true;

    private final TransportRetry retry = new TransportRetry(new TransportRetry.Host() {
        @Override
        public void postDelayed(Runnable task, long delayMillis) {
            timers.add(task);
            delays.add(delayMillis);
        }

        @Override
        public void removeCallbacks(Runnable task) {
            timers.remove(task);
        }

        @Override
        public boolean reload(String url) {
            if (!paired) return false;
            loads.add(url);
            return true;
        }
    });

    // Runs every timer still pending, as if its delay had passed.
    private void fireTimers() {
        List<Runnable> due = new ArrayList<>(timers);
        timers.clear();
        for (Runnable task : due) task.run();
    }

    @Test
    public void pairingNavigationCancelsARetryScheduledBeforeIt() {
        retry.failed(REMOTE);
        assertEquals(1, timers.size());
        retry.navigating(PAIRING);
        fireTimers();
        retry.now(true);
        assertEquals("the old retry replaced the pairing page", Collections.emptyList(), loads);
        assertNull(retry.url());
    }

    @Test
    public void retryWithoutPairingNavigationStillReloadsWithBackoff() {
        retry.failed(REMOTE);
        fireTimers();
        assertEquals(Collections.singletonList(REMOTE), loads);
        retry.failed(REMOTE);
        fireTimers();
        assertEquals(Arrays.asList(REMOTE, REMOTE), loads);
        assertEquals(Arrays.asList(1_000L, 2_000L), delays);
    }

    @Test
    public void navigatingWithinThePairedOriginKeepsTheRetry() {
        retry.failed(REMOTE);
        retry.navigating("https://moa.example/?session=other");
        fireTimers();
        assertEquals(Collections.singletonList(REMOTE), loads);
    }

    @Test
    public void retryStopsWhenThePageIsNoLongerPaired() {
        retry.failed(REMOTE);
        paired = false;
        fireTimers();
        assertEquals(Collections.emptyList(), loads);
        assertNull(retry.url());
    }
}
