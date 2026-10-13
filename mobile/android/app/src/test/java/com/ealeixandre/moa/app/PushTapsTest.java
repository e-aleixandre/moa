package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executor;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import org.junit.After;
import org.junit.Test;

// Review f2 P2: a tap with an expired credential forgets push, and forgetting
// waits on the lifecycle an incoming message holds around posting its
// notification. The tap's authentication must run on PushTaps' executor,
// never on the calling (UI) thread, and only a navigation still valid for
// the same generation and pairing comes back to the main thread.
//
// Owner decision: a tap with an expired credential always ends on the local
// pairing page, never on the paired origin, and its envelope is not opened.
public class PushTapsTest {

    private static final String ORIGIN = "https://moa.example";

    private final PushLifecycle lifecycle = new PushLifecycle();
    private final ExecutorService background = Executors.newSingleThreadExecutor(r -> new Thread(r, "push-taps"));
    private final List<Runnable> mainQueue = Collections.synchronizedList(new ArrayList<>());
    private final Executor main = mainQueue::add;
    private final PushTaps taps = new PushTaps(background, main);
    private final List<String> opened = Collections.synchronizedList(new ArrayList<>());
    private final List<String> envelopeReads = Collections.synchronizedList(new ArrayList<>());
    // Records every field read, so a test can tell whether the envelope was opened.
    private final Map<String, String> envelope = new HashMap<String, String>() {
        @Override
        public String get(Object key) {
            envelopeReads.add(String.valueOf(key));
            return super.get(key);
        }
    };
    private final PushEnvelope.Keys keys = PushEnvelope.deriveKeys(new byte[32]);
    private volatile boolean expired;
    // What the UI thread can know without Keystore: the cached expiry.
    private volatile boolean knownExpired;
    private volatile String coveredOn;
    private volatile String paired = ORIGIN;
    private volatile String forgotOn;

    // NativePushRegistration.currentKeys: an expired credential forgets
    // inside lifecycle.end before answering without keys.
    private final PushTaps.Source source = new PushTaps.Source() {
        @Override
        public int generation() {
            return lifecycle.generation();
        }

        @Override
        public PushEnvelope.Keys currentKeys() {
            if (expired) {
                lifecycle.end(() -> {
                    forgotOn = Thread.currentThread().getName();
                    return true;
                });
            }
            return keys;
        }

        @Override
        public boolean credentialExpired() {
            return expired;
        }

        @Override
        public boolean knownExpired() {
            return knownExpired;
        }

        @Override
        public String pairedOrigin() {
            return paired;
        }
    };

    private final PushTaps.Navigation navigation = new PushTaps.Navigation() {
        @Override
        public void open(PushEnvelope.Destination destination, String origin) {
            opened.add(destination.kind + "@" + origin);
        }

        @Override
        public void pair() {
            opened.add("pair");
        }

        @Override
        public void cover() {
            coveredOn = Thread.currentThread().getName();
            opened.add("cover");
        }

        @Override
        public void uncover() {
            opened.add("uncover");
        }
    };

    @After
    public void stop() {
        background.shutdownNow();
    }

    private void tap() throws InterruptedException {
        taps.handle(envelope, source, navigation);
        background.shutdown();
        assertTrue(background.awaitTermination(5, TimeUnit.SECONDS));
    }

    private void drainMain() {
        List<Runnable> queued;
        synchronized (mainQueue) {
            queued = new ArrayList<>(mainQueue);
            mainQueue.clear();
        }
        for (Runnable task : queued) task.run();
    }

    @Test
    public void expiredTapDoesNotWaitOnALifecycleHeldByAPost() throws Exception {
        CountDownLatch posting = new CountDownLatch(1);
        CountDownLatch release = new CountDownLatch(1);
        Thread receiving = new Thread(() -> lifecycle.publish(lifecycle.generation(), () -> true, () -> {
            posting.countDown();
            try {
                release.await(5, TimeUnit.SECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }), "fcm-notify");
        Thread ui = new Thread(() -> taps.handle(envelope, source, navigation), "ui");
        try {
            receiving.start();
            assertTrue(posting.await(5, TimeUnit.SECONDS));
            expired = true;
            ui.start();
            ui.join(1000);
            assertFalse("the tap waited on the push lifecycle on the UI thread", ui.isAlive());
        } finally {
            release.countDown();
            receiving.join(5000);
            ui.join(5000);
        }
        background.shutdown();
        assertTrue(background.awaitTermination(5, TimeUnit.SECONDS));
        assertEquals("push-taps", forgotOn);
        drainMain();
        // The forget ended the generation the tap started in: no destination
        // opens, but the expired tap still ends on the pairing page.
        assertEquals(Collections.singletonList("pair"), opened);
    }

    @Test
    public void expiredTapShowsPairingOnTheMainThreadWithoutOpeningTheEnvelope() throws Exception {
        expired = true;
        tap();
        assertEquals(Collections.emptyList(), opened);
        drainMain();
        assertEquals(Collections.singletonList("pair"), opened);
        assertEquals("the expired tap read its envelope", Collections.emptyList(), envelopeReads);
    }

    @Test
    public void expiredTapEndsOnPairingEvenWhenForgottenOrUnpairedBeforeTheMainThread() throws Exception {
        expired = true;
        tap();
        lifecycle.end(() -> true);
        paired = null;
        drainMain();
        assertEquals(Collections.singletonList("pair"), opened);
    }

    // The page already in the WebView (a session left open before the
    // credential expired) must not be drawn when the tap brings the app
    // back: the cover goes up on the calling (UI) thread, inside handle,
    // before any background work or main-thread step can run.
    @Test
    public void knownExpiredTapCoversOnTheCallingThreadBeforeAnythingElse() throws Exception {
        knownExpired = true;
        expired = true;
        CountDownLatch hold = new CountDownLatch(1);
        background.execute(() -> {
            try {
                hold.await(5, TimeUnit.SECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        });
        taps.handle(envelope, source, navigation);
        assertEquals(Collections.singletonList("cover"), opened);
        assertEquals(Thread.currentThread().getName(), coveredOn);
        hold.countDown();
        background.shutdown();
        assertTrue(background.awaitTermination(5, TimeUnit.SECONDS));
        drainMain();
        // Stays covered until the pairing page commits (PushNavigator).
        assertEquals(Arrays.asList("cover", "pair"), opened);
    }

    @Test
    public void validTapIsNeverCovered() throws Exception {
        tap();
        drainMain();
        assertEquals(Collections.singletonList("home@" + ORIGIN), opened);
        assertNull(coveredOn);
    }

    // A stale cache (the credential was replaced right after it was read)
    // must not leave the WebView hidden: the authoritative check uncovers.
    @Test
    public void coverIsLiftedWhenTheCredentialTurnsOutValid() throws Exception {
        knownExpired = true;
        tap();
        paired = "https://other.example";
        drainMain();
        assertEquals(Arrays.asList("cover", "uncover"), opened);
    }

    @Test
    public void currentTapNavigatesOnTheMainThread() throws Exception {
        tap();
        assertEquals(Collections.emptyList(), opened);
        drainMain();
        // An envelope that does not open with the current key leads home.
        assertEquals(Collections.singletonList("home@" + ORIGIN), opened);
        assertTrue("the current tap did not open its envelope", envelopeReads.contains("v"));
    }

    @Test
    public void tapIsDroppedWhenForgottenBeforeItReachesTheMainThread() throws Exception {
        tap();
        lifecycle.end(() -> true);
        drainMain();
        assertEquals(Collections.emptyList(), opened);
    }

    @Test
    public void tapIsDroppedWhenThePairingChangedBeforeItReachesTheMainThread() throws Exception {
        tap();
        paired = "https://other.example";
        drainMain();
        assertEquals(Collections.emptyList(), opened);
    }
}
