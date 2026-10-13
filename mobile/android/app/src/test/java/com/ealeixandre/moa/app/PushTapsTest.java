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
public class PushTapsTest {

    private static final String ORIGIN = "https://moa.example";

    private final PushLifecycle lifecycle = new PushLifecycle();
    private final ExecutorService background = Executors.newSingleThreadExecutor(r -> new Thread(r, "push-taps"));
    private final List<Runnable> mainQueue = Collections.synchronizedList(new ArrayList<>());
    private final Executor main = mainQueue::add;
    private final PushTaps taps = new PushTaps(background, main);
    private final List<String> opened = Collections.synchronizedList(new ArrayList<>());
    private final Map<String, String> envelope = new HashMap<>();
    private volatile boolean expired;
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
            return null;
        }

        @Override
        public String pairedOrigin() {
            return paired;
        }
    };

    private final PushTaps.Navigation navigation = (destination, origin) -> opened.add(destination.kind + "@" + origin);

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
        // The forget ended the generation the tap started in: nothing opens.
        assertEquals(Collections.emptyList(), opened);
    }

    @Test
    public void currentTapNavigatesOnTheMainThread() throws Exception {
        tap();
        assertEquals(Collections.emptyList(), opened);
        drainMain();
        assertEquals(Collections.singletonList("home@" + ORIGIN), opened);
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
