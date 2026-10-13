package com.ealeixandre.moa.app;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
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
    // The store cannot be read: credentialExpired throws, pairedOrigin is null.
    private volatile boolean unreadable;
    // Whether the in-memory cache (knownOrigin/knownValid) holds anything.
    private volatile boolean cacheKnown = true;
    private final List<String> originLoadsOn = Collections.synchronizedList(new ArrayList<>());
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
        public boolean credentialExpired() throws PushTaps.Unreadable {
            if (unreadable) throw new PushTaps.Unreadable();
            return expired;
        }

        // The loading getter: records where it ran.
        @Override
        public String pairedOrigin() {
            originLoadsOn.add(Thread.currentThread().getName());
            return unreadable && !cacheKnown ? null : paired;
        }

        @Override
        public String knownOrigin() {
            return cacheKnown ? paired : null;
        }

        @Override
        public boolean knownValid() {
            return cacheKnown && paired != null && !expired;
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

    // Re-review bb case 2: a pairing completed after the expired result was
    // queued (a claim saved a new, valid credential and its forget bumped the
    // generation) must not be undone by sending the page to ?unpaired=1.
    @Test
    public void expiredResultDoesNotUndoAPairingCompletedMeanwhile() throws Exception {
        expired = true;
        tap();
        expired = false;
        lifecycle.end(() -> true);
        drainMain();
        assertEquals(Collections.emptyList(), opened);
    }

    // Re-review bb case 3: unreadable store, nothing cached. The main-thread
    // step must not call the loading getter (Keystore on the UI thread), and
    // an unknown expiry ends on pairing, never on content.
    @Test
    public void unreadableUnknownStoreIsNotReadOnMainAndEndsOnPairing() throws Exception {
        unreadable = true;
        cacheKnown = false;
        paired = null;
        tap();
        drainMain();
        assertFalse("the loading getter ran on the main thread",
            originLoadsOn.contains(Thread.currentThread().getName()));
        assertEquals(Collections.singletonList("pair"), opened);
    }

    // Re-review bb case 4: a read error is not proof the credential is
    // valid. With the cached expiry saying expired, the tap ends on pairing.
    @Test
    public void readErrorWithCachedExpiryEndsOnPairingNotContent() throws Exception {
        expired = true;
        unreadable = true;
        tap();
        drainMain();
        assertEquals(Collections.singletonList("pair"), opened);
    }

    // The cached expiry is used when present: valid in memory, unreadable
    // now, the tap behaves as a valid one (home, nothing opened in the envelope).
    @Test
    public void readErrorWithCachedValidCredentialIsNotTreatedAsExpired() throws Exception {
        unreadable = true;
        tap();
        drainMain();
        assertEquals(Collections.singletonList("home@" + ORIGIN), opened);
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
